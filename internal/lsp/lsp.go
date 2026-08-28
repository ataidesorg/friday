package lsp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultInitTimeout = 8 * time.Second
	defaultDiagTimeout = 2 * time.Second
	maxDiagLines       = 10
)

// Server declares one configured language server and the file extensions it
// covers. Like MCP servers, the command is user-config trust and spawns
// directly rather than through the run_command sandbox: a language server is
// a long-lived stdio process, not a one-shot command.
type Server struct {
	Name       string
	Command    []string
	Extensions []string
}

// Manager owns at most one running client per configured server, started on
// first use. Any failure marks the server broken: the warning is returned
// in-band exactly once (so it lands in tool output on every UI, never on a
// raw stderr that would tear the TUI), and diagnostics stay off after that.
type Manager struct {
	root    string
	servers []Server

	mu      sync.Mutex
	clients map[string]*client
	broken  map[string]bool
	open    map[string]int // uri -> version already sent via didOpen

	initTimeout time.Duration
	diagTimeout time.Duration
	start       func(Server) (*client, error) // swapped for a pipe pair in tests
}

// NewManager builds a manager for the given servers rooted at the workspace.
func NewManager(root string, servers []Server) *Manager {
	m := &Manager{
		root:        root,
		servers:     servers,
		clients:     make(map[string]*client),
		broken:      make(map[string]bool),
		open:        make(map[string]int),
		initTimeout: defaultInitTimeout,
		diagTimeout: defaultDiagTimeout,
	}
	m.start = func(s Server) (*client, error) { return startProcess(root, s) }
	return m
}

// Diagnose reports post-edit diagnostics for one absolute path, or "". It
// never returns an error: a broken server yields one in-band warning and
// then silence. The whole call runs under a hard deadline so a wedged
// server (even one blocking our writes) cannot stall the tool.
func (m *Manager) Diagnose(ctx context.Context, path string) string {
	if m == nil {
		return ""
	}
	srv, ok := m.serverFor(path)
	if !ok {
		return ""
	}
	m.mu.Lock()
	if m.broken[srv.Name] {
		m.mu.Unlock()
		return ""
	}
	m.mu.Unlock()

	done := make(chan string, 1)
	go func() { done <- m.diagnose(ctx, srv, path) }()
	select {
	case s := <-done:
		return s
	case <-time.After(m.initTimeout + 2*m.diagTimeout):
		return m.disable(srv.Name, "server stopped responding")
	case <-ctx.Done():
		return ""
	}
}

// Close kills every running server. Language servers survive an abrupt kill
// cleanly, so no shutdown handshake.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		c.close()
	}
	m.clients = make(map[string]*client)
}

func (m *Manager) serverFor(path string) (Server, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return Server{}, false
	}
	for _, s := range m.servers {
		for _, e := range s.Extensions {
			if strings.ToLower(e) == ext {
				return s, true
			}
		}
	}
	return Server{}, false
}

func (m *Manager) diagnose(ctx context.Context, srv Server, path string) string {
	c, warn := m.client(ctx, srv)
	if warn != "" || c == nil {
		return warn
	}
	body, err := os.ReadFile(path) //nolint:gosec // path was just written by a tool inside the workspace
	if err != nil {
		return ""
	}
	uri := "file://" + filepath.ToSlash(path)
	ch := c.armDiags(uri)

	m.mu.Lock()
	version := m.open[uri] + 1
	m.open[uri] = version
	m.mu.Unlock()
	if version == 1 {
		err = c.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri": uri, "languageId": languageID(path), "version": version, "text": string(body),
			},
		})
	} else {
		err = c.notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": version},
			"contentChanges": []map[string]any{{"text": string(body)}},
		})
	}
	if err != nil {
		return m.disable(srv.Name, err.Error())
	}
	select {
	case diags := <-ch:
		return render(m.root, path, diags)
	case <-c.dead:
		return m.disable(srv.Name, "server exited")
	case <-time.After(m.diagTimeout):
		return "" // slow or silent server is normal, never worth blocking on
	case <-ctx.Done():
		return ""
	}
}

// client returns the running client for srv, starting and initializing it on
// first use. The second return is the one-time warning on failure.
func (m *Manager) client(ctx context.Context, srv Server) (*client, string) {
	m.mu.Lock()
	if c, ok := m.clients[srv.Name]; ok {
		m.mu.Unlock()
		select {
		case <-c.dead:
			return nil, m.disable(srv.Name, "server exited")
		default:
			return c, ""
		}
	}
	m.mu.Unlock()

	c, err := m.start(srv)
	if err != nil {
		return nil, m.disable(srv.Name, err.Error())
	}
	ictx, cancel := context.WithTimeout(ctx, m.initTimeout)
	defer cancel()
	rootURI := "file://" + filepath.ToSlash(m.root)
	_, err = c.call(ictx, "initialize", map[string]any{
		"processId":    os.Getpid(),
		"rootUri":      rootURI,
		"capabilities": map[string]any{"textDocument": map[string]any{"publishDiagnostics": map[string]any{}}},
		"workspaceFolders": []map[string]any{
			{"uri": rootURI, "name": filepath.Base(m.root)},
		},
	})
	if err == nil {
		err = c.notify("initialized", map[string]any{})
	}
	if err != nil {
		c.close()
		return nil, m.disable(srv.Name, err.Error())
	}
	m.mu.Lock()
	m.clients[srv.Name] = c
	m.mu.Unlock()
	return c, ""
}

// disable marks a server broken and returns its single in-band warning;
// later calls for that server return "".
func (m *Manager) disable(name, reason string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[name]; ok {
		c.close()
		delete(m.clients, name)
	}
	if m.broken[name] {
		return ""
	}
	m.broken[name] = true
	return fmt.Sprintf("lsp %s: diagnostics disabled (%s)", name, reason)
}

func render(root, path string, diags []diagnostic) string {
	rel := path
	if r, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(r, "..") {
		rel = r
	}
	var lines []string
	for _, d := range diags {
		if d.Severity > 2 { // hints and info stay out of the model's face
			continue
		}
		kind := "error"
		if d.Severity == 2 {
			kind = "warning"
		}
		msg := strings.ReplaceAll(d.Message, "\n", " ")
		lines = append(lines, fmt.Sprintf("%s %s:%d:%d %s", kind, rel, d.Range.Start.Line+1, d.Range.Start.Character+1, msg))
	}
	if len(lines) == 0 {
		return ""
	}
	if extra := len(lines) - maxDiagLines; extra > 0 {
		lines = append(lines[:maxDiagLines], fmt.Sprintf("…and %d more", extra))
	}
	return strings.Join(lines, "\n")
}

// languageID maps common extensions to LSP language ids; anything else uses
// the bare extension, which real servers accept for their own filetypes.
func languageID(path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	switch ext {
	case "py":
		return "python"
	case "ts":
		return "typescript"
	case "tsx":
		return "typescriptreact"
	case "js":
		return "javascript"
	case "jsx":
		return "javascriptreact"
	case "rs":
		return "rust"
	}
	return ext
}

func startProcess(root string, s Server) (*client, error) {
	if len(s.Command) == 0 {
		return nil, fmt.Errorf("lsp server %s has no command", s.Name)
	}
	cmd := exec.Command(s.Command[0], s.Command[1:]...) //nolint:gosec // command comes from user-layer config the owner wrote
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	stop := func() {
		_ = cmd.Process.Kill()
	}
	go func() { _ = cmd.Wait() }() // reap; the read loop observes the closed pipe
	return newClient(stdin, stdout, stop), nil
}
