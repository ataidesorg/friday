package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func connectChat(o Options) ChatModel {
	o.Width, o.NoColor = 80, true
	if o.Providers == nil {
		o.Providers = []ProviderInfo{{Name: "fireworks", Detail: "api.fireworks.ai", KeyURL: "https://app.fireworks.ai"}}
	}
	m := NewChat(o, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(ChatModel)
}

// deliver executes the wizard's pending async command and feeds its
// message back through Update, following one hop of the fetch or sign-in
// chain.
func deliver(t *testing.T, m ChatModel, cmd tea.Cmd) (ChatModel, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a pending command")
	}
	msg := cmd()
	if msg == nil {
		return m, nil
	}
	next, ncmd := m.Update(msg)
	return next.(ChatModel), ncmd
}

// /connect opens the picker; a registry provider asks for the key first
// (masked, with the vendor's key page in the hint), validates it by
// fetching the live model list, and the model is picked from that list.
func TestConnectWizardRegistry(t *testing.T) {
	var got ConnectRequest
	var probed ConnectRequest
	m := connectChat(Options{
		Connect: func(req ConnectRequest) (RouteInfo, error) {
			got = req
			return RouteInfo{Name: "fireworks", Provider: "fireworks", Model: req.Model}, nil
		},
		ConnectModels: func(req ConnectRequest) ([]string, string) {
			probed = req
			return []string{"qwen3", "llama"}, ""
		},
	})
	next, _ := typeText(m, "/connect").Update(enter())
	m = next.(ChatModel)
	if m.ov == nil || m.ov.kind != overlayConnect {
		t.Fatalf("picker not open: %+v", m.ov)
	}
	if v := m.View(); !strings.Contains(v, "fireworks") || !strings.Contains(v, "custom endpoint") {
		t.Fatalf("picker rows missing:\n%s", v)
	}
	next, _ = m.Update(enter()) // cursor starts on fireworks
	m = next.(ChatModel)
	if m.conn == nil || m.conn.step != connectStepKey || m.conn.provider != "fireworks" {
		t.Fatalf("wizard not at the key step: %+v", m.conn)
	}
	if v := m.View(); !strings.Contains(v, "get one at https://app.fireworks.ai") {
		t.Fatalf("key hint missing:\n%s", v)
	}
	next, _ = m.Update(keyRunes("sk-supersecret"))
	m = next.(ChatModel)
	if v := m.View(); strings.Contains(v, "sk-supersecret") || !strings.Contains(v, "•") {
		t.Fatalf("key echoed instead of masked:\n%s", v)
	}
	next, cmd := m.Update(enter())
	m = next.(ChatModel)
	if m.conn.step != connectStepFetch {
		t.Fatalf("submit did not start the fetch: %+v", m.conn)
	}
	m, _ = deliver(t, m, cmd)
	if probed.Provider != "fireworks" || probed.Key != "sk-supersecret" {
		t.Fatalf("fetch probed with %+v", probed)
	}
	if m.ov == nil || m.ov.kind != overlayConnectModel {
		t.Fatalf("model picker not open: %+v", m.ov)
	}
	if v := m.View(); !strings.Contains(v, "qwen3") || !strings.Contains(v, "type a model id") {
		t.Fatalf("model rows missing:\n%s", v)
	}
	next, _ = m.Update(enter()) // cursor starts on qwen3
	m = next.(ChatModel)
	if got != (ConnectRequest{Provider: "fireworks", Model: "qwen3", Key: "sk-supersecret"}) {
		t.Fatalf("callback got %+v", got)
	}
	if m.conn != nil || m.ov != nil {
		t.Fatalf("wizard still open after success")
	}
	if m.active != "fireworks" || m.route != "fireworks/qwen3" {
		t.Fatalf("route not switched: active %q route %q", m.active, m.route)
	}
	joined := strings.Join(m.Lines, "\n")
	if !strings.Contains(joined, "connected fireworks") {
		t.Fatalf("no confirmation line:\n%s", joined)
	}
	if strings.Contains(joined, "sk-supersecret") {
		t.Fatalf("key leaked into the scrollback:\n%s", joined)
	}
}

// The custom row starts at the base URL step, rejects a non-http(s) URL,
// then asks for the key; an empty fetch (with a note) falls back to the
// typed model id.
func TestConnectWizardCustomURL(t *testing.T) {
	var got ConnectRequest
	m := connectChat(Options{
		Connect: func(req ConnectRequest) (RouteInfo, error) {
			got = req
			return RouteInfo{Name: "custom", Provider: "custom", Model: req.Model}, nil
		},
		ConnectModels: func(ConnectRequest) ([]string, string) { return nil, "endpoint has no /models" },
	})
	next, _ := m.startConnect(connectCustom)
	m = next.(ChatModel)
	if m.conn.step != connectStepURL {
		t.Fatalf("custom flow not at the URL step: %+v", m.conn)
	}
	next, _ = m.Update(keyRunes("nope"))
	next, _ = next.(ChatModel).Update(enter())
	m = next.(ChatModel)
	if m.conn == nil || m.conn.step != connectStepURL {
		t.Fatalf("bad URL advanced the wizard: %+v", m.conn)
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "base URL must be") {
		t.Fatalf("no URL warning: %v", m.Lines)
	}
	m.conn.buf = nil
	next, _ = m.Update(keyRunes("https://api.groq.com/openai/v1"))
	next, _ = next.(ChatModel).Update(enter())
	m = next.(ChatModel)
	if m.conn.step != connectStepKey || m.conn.baseURL != "https://api.groq.com/openai/v1" {
		t.Fatalf("valid URL did not advance to the key: %+v", m.conn)
	}
	next, _ = m.Update(keyRunes("sk-x"))
	next, cmd := next.(ChatModel).Update(enter())
	m, _ = deliver(t, next.(ChatModel), cmd)
	if m.conn == nil || m.conn.step != connectStepModel || m.ov != nil {
		t.Fatalf("empty fetch did not fall back to typed model: %+v ov=%v", m.conn, m.ov)
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "endpoint has no /models") {
		t.Fatalf("fetch note not shown: %v", m.Lines)
	}
	next, _ = m.Update(keyRunes("qwen3"))
	next, _ = next.(ChatModel).Update(enter())
	m = next.(ChatModel)
	if got.BaseURL != "https://api.groq.com/openai/v1" || got.Model != "qwen3" || got.Key != "sk-x" {
		t.Fatalf("callback got %+v", got)
	}
	if m.conn != nil {
		t.Fatalf("wizard still open after success")
	}
}

// The model overlay's manual row drops to the typed-id step instead of
// committing a model.
func TestConnectManualRow(t *testing.T) {
	m := connectChat(Options{
		Connect:       func(ConnectRequest) (RouteInfo, error) { return RouteInfo{}, nil },
		ConnectModels: func(ConnectRequest) ([]string, string) { return []string{"a"}, "" },
	})
	next, _ := m.startConnect("fireworks")
	next, _ = next.(ChatModel).Update(keyRunes("sk-x"))
	next, cmd := next.(ChatModel).Update(enter())
	m, _ = deliver(t, next.(ChatModel), cmd)
	next, _ = m.connectModelPicked(connectManual)
	m = next.(ChatModel)
	if m.conn == nil || m.conn.step != connectStepModel {
		t.Fatalf("manual row did not open the typed step: %+v", m.conn)
	}
}

// Esc cancels the wizard without calling the callback and zeroes the held
// key; esc on the model overlay cancels the whole wizard too, and a stale
// fetch message from the cancelled run is dropped.
func TestConnectEscCancels(t *testing.T) {
	called := false
	m := connectChat(Options{
		Connect:       func(ConnectRequest) (RouteInfo, error) { called = true; return RouteInfo{}, nil },
		ConnectModels: func(ConnectRequest) ([]string, string) { return []string{"a"}, "" },
	})
	next, _ := m.startConnect("fireworks")
	next, _ = next.(ChatModel).Update(keyRunes("sk-half"))
	next, cmd := next.(ChatModel).Update(enter()) // fetch pending
	m = next.(ChatModel)
	keyRef := m.conn.key
	gen := m.conn.gen
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(ChatModel)
	if m.conn != nil || called {
		t.Fatalf("esc left wizard open (%v) or called connect (%v)", m.conn, called)
	}
	for _, r := range keyRef {
		if r != 0 {
			t.Fatalf("key not zeroed on cancel: %q", string(keyRef))
		}
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "connect cancelled") {
		t.Fatalf("no cancel line: %v", m.Lines)
	}
	next, _ = m.Update(connectModelsMsg{gen: gen, models: []string{"a"}})
	m = next.(ChatModel)
	if m.ov != nil {
		t.Fatalf("stale fetch reopened the wizard: %+v", m.ov)
	}
	_ = cmd

	// Esc on the model overlay cancels the wizard, not just the overlay.
	m2 := connectChat(Options{
		Connect:       func(ConnectRequest) (RouteInfo, error) { return RouteInfo{}, nil },
		ConnectModels: func(ConnectRequest) ([]string, string) { return []string{"a"}, "" },
	})
	next, _ = m2.startConnect("fireworks")
	next, _ = next.(ChatModel).Update(keyRunes("sk-x"))
	next, cmd = next.(ChatModel).Update(enter())
	m2, _ = deliver(t, next.(ChatModel), cmd)
	if m2.ov == nil || m2.ov.kind != overlayConnectModel {
		t.Fatalf("model overlay not open: %+v", m2.ov)
	}
	next, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 = next.(ChatModel)
	if m2.conn != nil || m2.ov != nil {
		t.Fatalf("overlay esc left wizard state: conn=%v ov=%v", m2.conn, m2.ov)
	}
}

// An OAuth provider confirms the recorded risk, signs in on a background
// flow that streams progress lines, then fetches the catalog with the
// stored token — the request carries no key.
func TestConnectOAuthFlow(t *testing.T) {
	var got, probed ConnectRequest
	m := connectChat(Options{
		Providers: []ProviderInfo{{Name: "codex", OAuth: true, Risk: "codex uses a vendor-prohibited flow"}},
		Connect: func(req ConnectRequest) (RouteInfo, error) {
			got = req
			return RouteInfo{Name: "codex", Provider: "codex", Model: req.Model}, nil
		},
		ConnectModels: func(req ConnectRequest) ([]string, string) {
			probed = req
			return []string{"gpt-5"}, ""
		},
		Login: func(_ context.Context, _ string, progress func(string)) error {
			progress("open https://auth.example/device")
			return nil
		},
	})
	next, _ := m.startConnect("codex")
	m = next.(ChatModel)
	if m.conn == nil || m.conn.step != connectStepRisk {
		t.Fatalf("risk step skipped: %+v", m.conn)
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "vendor-prohibited flow") {
		t.Fatalf("risk note not shown: %v", m.Lines)
	}
	next, cmd := m.Update(enter()) // accept risk, start sign-in
	m = next.(ChatModel)
	if m.conn.step != connectStepLogin {
		t.Fatalf("sign-in not started: %+v", m.conn)
	}
	m, cmd = deliver(t, m, cmd) // progress note
	if !strings.Contains(strings.Join(m.Lines, "\n"), "auth.example/device") {
		t.Fatalf("progress line not shown: %v", m.Lines)
	}
	m, cmd = deliver(t, m, cmd) // login done -> fetch pending
	if !strings.Contains(strings.Join(m.Lines, "\n"), "signed in to codex") {
		t.Fatalf("no signed-in line: %v", m.Lines)
	}
	m, _ = deliver(t, m, cmd) // catalog -> model overlay
	if probed.Provider != "codex" || probed.Key != "" {
		t.Fatalf("fetch probed with %+v", probed)
	}
	if m.ov == nil || m.ov.kind != overlayConnectModel {
		t.Fatalf("model picker not open: %+v", m.ov)
	}
	next, _ = m.Update(enter())
	m = next.(ChatModel)
	if got != (ConnectRequest{Provider: "codex", Model: "gpt-5"}) {
		t.Fatalf("callback got %+v", got)
	}
	if m.conn != nil {
		t.Fatalf("wizard still open after success")
	}
}

// A failed sign-in closes the wizard; a stale login message after cancel
// is dropped; a session without the Login callback refuses OAuth rows.
func TestConnectLoginFailures(t *testing.T) {
	oauth := []ProviderInfo{{Name: "codex", OAuth: true}}
	m := connectChat(Options{
		Providers: oauth,
		Connect:   func(ConnectRequest) (RouteInfo, error) { return RouteInfo{}, nil },
		Login: func(_ context.Context, _ string, _ func(string)) error {
			return errors.New("browser flow denied")
		},
	})
	next, cmd := m.startConnect("codex") // no risk note: straight to sign-in
	m = next.(ChatModel)
	if m.conn == nil || m.conn.step != connectStepLogin {
		t.Fatalf("sign-in not started: %+v", m.conn)
	}
	m, _ = deliver(t, m, cmd)
	if m.conn != nil || !strings.Contains(strings.Join(m.Lines, "\n"), "sign-in failed: browser flow denied") {
		t.Fatalf("failed sign-in did not close: conn=%v lines=%v", m.conn, m.Lines)
	}

	blocked := make(chan struct{})
	m2 := connectChat(Options{
		Providers: oauth,
		Connect:   func(ConnectRequest) (RouteInfo, error) { return RouteInfo{}, nil },
		Login: func(ctx context.Context, _ string, _ func(string)) error {
			<-blocked
			return ctx.Err()
		},
	})
	next, _ = m2.startConnect("codex")
	m2 = next.(ChatModel)
	gen := m2.conn.gen
	next, _ = m2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 = next.(ChatModel)
	close(blocked)
	next, _ = m2.Update(connectLoginMsg{gen: gen, err: errors.New("late")})
	m2 = next.(ChatModel)
	if m2.conn != nil || strings.Contains(strings.Join(m2.Lines, "\n"), "late") {
		t.Fatalf("stale login message acted after cancel: conn=%v lines=%v", m2.conn, m2.Lines)
	}

	m3 := connectChat(Options{
		Providers: oauth,
		Connect:   func(ConnectRequest) (RouteInfo, error) { return RouteInfo{}, nil },
	})
	next, _ = m3.startConnect("codex")
	m3 = next.(ChatModel)
	if m3.conn != nil || !strings.Contains(strings.Join(m3.Lines, "\n"), "sign-in is not available") {
		t.Fatalf("nil Login not refused: conn=%v lines=%v", m3.conn, m3.Lines)
	}
}

// A failed connect closes the wizard with the error — the failure is a
// config or store write, not a typo worth holding the key step for.
func TestConnectErrorClosesWizard(t *testing.T) {
	m := connectChat(Options{
		Connect: func(ConnectRequest) (RouteInfo, error) {
			return RouteInfo{}, errors.New("store unavailable")
		},
	})
	next, _ := m.startConnect("fireworks")
	next, _ = next.(ChatModel).Update(keyRunes("sk-x"))
	next, _ = next.(ChatModel).Update(enter()) // nil ConnectModels: straight to model step
	next, _ = next.(ChatModel).Update(keyRunes("m1"))
	next, _ = next.(ChatModel).Update(enter())
	m = next.(ChatModel)
	if m.conn != nil {
		t.Fatalf("failed connect left the wizard open: %+v", m.conn)
	}
	if !strings.Contains(strings.Join(m.Lines, "\n"), "connect failed: store unavailable") {
		t.Fatalf("no failure line: %v", m.Lines)
	}
}

// Without a Connect callback the command reports itself unavailable, and a
// running turn refuses the wizard.
func TestConnectGuards(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	next, _ := typeText(m, "/connect").Update(enter())
	m = next.(ChatModel)
	if m.ov != nil || !strings.Contains(strings.Join(m.Lines, "\n"), "connect is not available") {
		t.Fatalf("nil callback did not warn: ov=%v lines=%v", m.ov, m.Lines)
	}
	busy := connectChat(Options{Connect: func(ConnectRequest) (RouteInfo, error) { return RouteInfo{}, nil }})
	busy.Running = true
	next, _ = busy.openConnect()
	busy = next.(ChatModel)
	if busy.ov != nil || !strings.Contains(strings.Join(busy.Lines, "\n"), "finish or cancel") {
		t.Fatalf("mid-turn connect not refused: ov=%v lines=%v", busy.ov, busy.Lines)
	}
}

// The typeahead menu offers /connect but hides itself while the wizard has
// the prompt, and ctrl+u clears the field being typed.
func TestConnectMenuAndEditing(t *testing.T) {
	m := connectChat(Options{Connect: func(ConnectRequest) (RouteInfo, error) { return RouteInfo{}, nil }})
	found := false
	for _, e := range typeText(m, "/con").slashMatches() {
		found = found || e.name == "connect"
	}
	if !found {
		t.Fatalf("/con does not offer connect")
	}
	next, _ := m.startConnect("fireworks")
	m = next.(ChatModel)
	if m.slashMatches() != nil {
		t.Fatalf("typeahead active under the wizard")
	}
	next, _ = m.Update(keyRunes("abc"))
	next, _ = next.(ChatModel).Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(ChatModel)
	if string(m.conn.buf) != "ab" {
		t.Fatalf("backspace got %q", string(m.conn.buf))
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m = next.(ChatModel)
	if len(m.conn.buf) != 0 {
		t.Fatalf("ctrl+u left %q", string(m.conn.buf))
	}
}
