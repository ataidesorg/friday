package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/ataidesorg/friday/internal/core"
)

// fakeServer speaks just enough MCP over pipes: initialize, tools/list with
// one echo tool, tools/call echoing the arguments back.
func fakeServer(t *testing.T, in io.Reader, out io.Writer) {
	t.Helper()
	sc := bufio.NewScanner(in)
	reply := func(id int64, result string) {
		fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%d,"result":%s}`+"\n", id, result)
	}
	for sc.Scan() {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		switch req.Method {
		case "initialize":
			reply(*req.ID, `{"protocolVersion":"2025-03-26","capabilities":{},"serverInfo":{"name":"fake"}}`)
		case "tools/list":
			reply(*req.ID, `{"tools":[{"name":"echo","description":"echo the input","inputSchema":{"type":"object"}}]}`)
		case "tools/call":
			var p struct {
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			body, _ := json.Marshal(fmt.Sprintf("echo:%s", p.Arguments))
			reply(*req.ID, `{"content":[{"type":"text","text":`+string(body)+`}],"isError":false}`)
		}
	}
}

func pipes(t *testing.T) *Client {
	t.Helper()
	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()
	go fakeServer(t, serverIn, serverOut)
	c := NewClient("srv", clientIn, clientOut)
	t.Cleanup(func() { _ = clientOut.Close(); _ = serverOut.Close() })
	return c
}

func TestClientBridge(t *testing.T) {
	c := pipes(t)
	if err := c.Initialize(); err != nil {
		t.Fatal(err)
	}
	defs, err := c.ListTools()
	if err != nil || len(defs) != 1 || defs[0].Name != "echo" {
		t.Fatalf("tools: %+v %v", defs, err)
	}
	ts := c.Tools(defs)
	sp := ts[0].Spec()
	if sp.Name != "mcp_srv_echo" || sp.Risk != core.RiskExecuteLocal || sp.Description != "echo the input" {
		t.Fatalf("spec: %+v", sp)
	}
	out, err := ts[0].Invoke(context.Background(), core.ToolInput{Call: core.NewToolCallID(), Arguments: json.RawMessage(`{"msg":"hi"}`)}, core.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Content, `echo:{"msg":"hi"}`) {
		t.Fatalf("content: %q", out.Content)
	}
	if out.CapabilitiesUsed[0].Scope.Kind != core.ScopeAny {
		t.Fatalf("scope: %+v", out.CapabilitiesUsed)
	}
}

func TestClientDeadServer(t *testing.T) {
	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, serverIn) }() // drain writes; never answer
	_ = serverOut.Close()                                // server gone before the first byte
	c := NewClient("srv", clientIn, clientOut)
	if _, _, err := c.CallTool("echo", nil); !errors.Is(err, core.ErrUnavailable) || !strings.Contains(err.Error(), "mcp server srv is not running") {
		t.Fatalf("first call: %v", err)
	}
	// Dead is sticky: later calls fail fast without touching the transport.
	if _, err := c.ListTools(); !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("second call: %v", err)
	}
}

func TestClientClose(t *testing.T) {
	c := pipes(t)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.CallTool("echo", nil); !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("after close: %v", err)
	}
}

// A server that never answers must not wedge Close: stopping the process
// (here: closing the pipes) releases the blocked call, then Close returns.
func TestCloseUnblocksHungCall(t *testing.T) {
	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()
	go func() { // swallow the request, never reply
		sc := bufio.NewScanner(serverIn)
		for sc.Scan() {
		}
	}()
	c := NewClient("hung", clientIn, clientOut)
	c.stop = func() error {
		_ = clientOut.Close()
		_ = serverOut.Close()
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := c.CallTool("echo", nil)
		done <- err
	}()
	if err := c.Close(); err != nil { // the -timeout flag is the watchdog
		t.Fatalf("close returned %v", err)
	}
	if err := <-done; !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("hung call returned %v", err)
	}
}
