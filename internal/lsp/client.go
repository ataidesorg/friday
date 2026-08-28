// Package lsp feeds language-server diagnostics into tool results after
// edits. Servers are config-declared and off by default; a crashed or
// misbehaving server disables its diagnostics with one warning and never
// fails the tool that triggered it.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// client speaks the slice of JSON-RPC 2.0 over Content-Length framing that
// diagnostics need: initialize, didOpen/didChange, publishDiagnostics.
type client struct {
	mu      sync.Mutex
	w       io.Writer
	nextID  int64
	pending map[int64]chan json.RawMessage
	diags   map[string]chan []diagnostic // armed per uri before a change is sent
	dead    chan struct{}                // closed when the read loop exits
	stop    func()                       // kills the server process; nil under test
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type diagnostic struct {
	Range struct {
		Start struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"start"`
	} `json:"range"`
	Severity int    `json:"severity"`
	Message  string `json:"message"`
}

func newClient(w io.Writer, r io.Reader, stop func()) *client {
	c := &client{
		w:       w,
		pending: make(map[int64]chan json.RawMessage),
		diags:   make(map[string]chan []diagnostic),
		dead:    make(chan struct{}),
		stop:    stop,
	}
	go c.readLoop(r)
	return c
}

// call sends a request and waits for its response, the context, or the server dying.
func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case res := <-ch:
		return res, nil
	case <-c.dead:
		return nil, fmt.Errorf("lsp server exited during %s", method)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *client) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// armDiags readies a fresh diagnostics slot for uri; send the change after
// arming so a fast publish is never missed.
func (c *client) armDiags(uri string) chan []diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan []diagnostic, 1)
	c.diags[uri] = ch
	return ch
}

// write emits one framed message; the caller holds c.mu.
func (c *client) write(msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n", len(b)); err != nil {
		return err
	}
	_, err = c.w.Write(b)
	return err
}

// readLoop routes responses to callers and publishDiagnostics to armed
// slots, and answers any server-to-client request with null so a chatty
// server never stalls. It exits (closing dead) on any read error.
func (c *client) readLoop(r io.Reader) {
	defer close(c.dead)
	br := bufio.NewReader(r)
	for {
		body, err := readFrame(br)
		if err != nil {
			return
		}
		var msg rpcMessage
		if json.Unmarshal(body, &msg) != nil {
			continue
		}
		switch {
		case msg.Method == "textDocument/publishDiagnostics":
			var p struct {
				URI         string       `json:"uri"`
				Diagnostics []diagnostic `json:"diagnostics"`
			}
			if json.Unmarshal(msg.Params, &p) != nil {
				continue
			}
			c.mu.Lock()
			if ch, ok := c.diags[p.URI]; ok {
				select {
				case ch <- p.Diagnostics:
				default: // an unread earlier publish stays; the waiter reads one
				}
			}
			c.mu.Unlock()
		case msg.Method != "" && len(msg.ID) > 0:
			// A server-to-client request (configuration, registrations…):
			// answer null rather than leave the server waiting.
			c.mu.Lock()
			_ = c.write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": nil})
			c.mu.Unlock()
		case len(msg.ID) > 0:
			var id int64
			if json.Unmarshal(msg.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch, ok := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ok {
				ch <- msg.Result
			}
		}
	}
}

func (c *client) close() {
	if c.stop != nil {
		c.stop()
	}
}

// readFrame reads one Content-Length framed body.
func readFrame(br *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
			if length, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
				return nil, err
			}
		}
	}
	if length < 0 || length > 16<<20 {
		return nil, fmt.Errorf("lsp frame without a sane Content-Length")
	}
	body := make([]byte, length)
	_, err := io.ReadFull(br, body)
	return body, err
}
