// Package mcp speaks the Model Context Protocol over stdio: newline-delimited
// JSON-RPC 2.0 to a config-declared server process, its tools bridged into
// the registry under mcp_<server>_<tool>. No network transports.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/ataidesorg/friday/internal/core"
)

const protocolVersion = "2025-03-26"

// ToolDef is one tool a server advertises.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Client is a single-server MCP client. Calls are serialized; a transport
// failure marks the client dead and every later call fails fast.
// ponytail: blocking reads, one call in flight; per-call deadlines if a
// hung server ever matters.
type Client struct {
	name string
	mu   sync.Mutex
	w    io.Writer
	sc   *bufio.Scanner
	id   int64
	dead error

	stopMu sync.Mutex
	stop   func() error
}

// NewClient wraps an existing transport (the server's stdout and stdin).
func NewClient(name string, r io.Reader, w io.Writer) *Client {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return &Client{name: name, w: w, sc: sc}
}

// Name returns the config name of the server.
func (c *Client) Name() string { return c.name }

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     *int64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) die(cause error) error {
	if c.dead == nil {
		if cause != nil {
			c.dead = fmt.Errorf("%w: mcp server %s: %w", core.ErrUnavailable, c.name, cause)
		} else {
			c.dead = fmt.Errorf("%w: mcp server %s is not running", core.ErrUnavailable, c.name)
		}
	}
	return c.dead
}

func (c *Client) send(req rpcRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("mcp %s: encode %s: %w", c.name, req.Method, err)
	}
	if _, err := c.w.Write(append(b, '\n')); err != nil {
		return c.die(err)
	}
	return nil
}

// call sends one request and blocks for its response, skipping any
// notifications or server-initiated requests in between.
func (c *Client) call(method string, params any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead != nil {
		return c.dead
	}
	c.id++
	id := c.id
	if err := c.send(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return err
	}
	for {
		if !c.sc.Scan() {
			return c.die(c.sc.Err())
		}
		line := bytes.TrimSpace(c.sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if json.Unmarshal(line, &resp) != nil || resp.ID == nil || *resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return fmt.Errorf("mcp %s: %s: %s", c.name, method, resp.Error.Message)
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("mcp %s: %s result: %w", c.name, method, err)
		}
		return nil
	}
}

// Initialize performs the MCP handshake.
func (c *Client) Initialize() error {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "friday"},
	}
	var discard json.RawMessage
	if err := c.call("initialize", params, &discard); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead != nil {
		return c.dead
	}
	return c.send(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
}

// ListTools fetches the server's tools.
// ponytail: first page only; cursor pagination when a server needs it.
func (c *Client) ListTools() ([]ToolDef, error) {
	var res struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := c.call("tools/list", nil, &res); err != nil {
		return nil, err
	}
	return res.Tools, nil
}

// CallTool invokes one tool; the joined text content and the server's
// isError flag come back.
func (c *Client) CallTool(tool string, args json.RawMessage) (string, bool, error) {
	params := struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}{Name: tool, Arguments: args}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := c.call("tools/call", params, &res); err != nil {
		return "", false, err
	}
	var parts []string
	for _, p := range res.Content {
		if p.Type == "text" && p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n"), res.IsError, nil
}

// Close stops the server process first — killing it unblocks any call stuck
// reading its stdout — then marks the client dead. Never takes c.mu before
// the process is down, so a hung server can't wedge shutdown.
func (c *Client) Close() error {
	c.stopMu.Lock()
	stop := c.stop
	c.stop = nil
	c.stopMu.Unlock()
	var err error
	if stop != nil {
		err = stop()
	}
	c.mu.Lock()
	if c.dead == nil {
		c.dead = fmt.Errorf("%w: mcp server %s is closed", core.ErrUnavailable, c.name)
	}
	c.mu.Unlock()
	return err
}

// Tools bridges the server's tools into registry-ready core.Tools named
// mcp_<server>_<tool>. Policy and the approval flow gate them by that name;
// their scope is any-resource, so only listings and posture apply.
func (c *Client) Tools(defs []ToolDef) []core.Tool {
	out := make([]core.Tool, 0, len(defs))
	for _, d := range defs {
		out = append(out, &bridged{c: c, def: d})
	}
	return out
}

type bridged struct {
	c   *Client
	def ToolDef
}

func (b *bridged) Spec() core.ToolSpec {
	schema := b.def.InputSchema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	desc := b.def.Description
	if desc == "" {
		desc = "MCP tool " + b.def.Name + " from server " + b.c.name
	}
	return core.ToolSpec{Name: "mcp_" + b.c.name + "_" + b.def.Name, Description: desc, Risk: core.RiskExecuteLocal, InputSchema: schema}
}

// Invoke forwards the call. A dead server errors fast; the tool loop turns
// that into model-visible data, so a crash degrades instead of killing the
// session.
func (b *bridged) Invoke(_ context.Context, in core.ToolInput, _ core.ToolContext) (core.ToolOutput, error) {
	text, isErr, err := b.c.CallTool(b.def.Name, in.Arguments)
	if err != nil {
		return core.ToolOutput{}, err
	}
	if isErr {
		text = "mcp tool error: " + text
	}
	return core.ToolOutput{Content: text, CapabilitiesUsed: []core.Capability{{Risk: core.RiskExecuteLocal, Scope: core.ResourceScope{Kind: core.ScopeAny}}}}, nil
}
