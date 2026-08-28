package tui

import (
	"context"
	"net/url"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ProviderInfo is one registry provider the /connect picker offers.
type ProviderInfo struct {
	Name   string // canonical registry id
	Detail string // shown beside the name, e.g. the endpoint host
	OAuth  bool   // browser sign-in instead of an API key
	Risk   string // non-empty: confirmed by the user before sign-in starts
	KeyURL string // where the vendor issues API keys (key-step hint)
}

// ConnectRequest is what the /connect wizard collected. Key is the secret:
// the callback registers it with the redactor and routes it to the secret
// store — it never lands in config files, the scrollback, or the session
// log, and the wizard renders it masked. An empty Key on a registry
// provider means an OAuth sign-in already stored tokens: only the route is
// written.
type ConnectRequest struct {
	Provider string // registry id; empty means a custom OpenAI-compatible endpoint
	BaseURL  string // custom endpoints only
	Model    string
	Key      string
}

// Wizard steps. A key-auth registry provider starts at the key step, a
// custom endpoint at the base URL, an OAuth provider at the risk
// confirmation (or straight into sign-in when the flow carries no risk
// note). The credential is then validated by fetching the provider's live
// model catalog with it, so the model is picked from what the account can
// actually reach — the typed-id step is only the fallback for providers
// with no listable catalog.
const (
	connectStepURL = iota
	connectStepKey
	connectStepRisk
	connectStepLogin
	connectStepFetch
	connectStepModel
)

// Picker row ids: the custom-endpoint flow, and the model overlay's
// escape-hatch row for ids the catalog does not list.
const (
	connectCustom = "__custom__"
	connectManual = "__manual__"
)

// connectState is an in-flight /connect wizard. The prompt box shows one
// field at a time; the key never enters the textarea and is zeroed on
// every exit path.
type connectState struct {
	provider string // chosen registry id; empty means custom
	oauth    bool
	risk     string
	keyURL   string
	step     int
	baseURL  string
	model    string
	key      []rune             // the secret, held until the final connect
	buf      []rune             // the field being typed; zeroed when it holds the key
	busy     string             // status text while sign-in or the catalog fetch runs
	gen      int                // stale async-message guard
	cancel   context.CancelFunc // stops a running sign-in
}

// typing reports whether the current step accepts text input.
func (c connectState) typing() bool {
	switch c.step {
	case connectStepURL, connectStepKey, connectStepModel:
		return true
	}
	return false
}

// connectModelsMsg delivers the wizard's async catalog fetch.
type connectModelsMsg struct {
	gen    int
	models []string
	note   string
}

// connectNoteMsg is one progress line from a running sign-in; it carries
// its channel so the handler can re-arm the listener.
type connectNoteMsg struct {
	gen  int
	line string
	ch   chan tea.Msg
}

// connectLoginMsg ends a sign-in; the listener is not re-armed after it.
type connectLoginMsg struct {
	gen int
	err error
}

// listenConn waits for the next message from a sign-in goroutine.
func listenConn(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// openConnect opens the provider picker for /connect.
func (m ChatModel) openConnect() (tea.Model, tea.Cmd) {
	if m.connectFn == nil {
		return m.append(tagWarn + " connect is not available"), nil
	}
	if m.Running {
		return m.append(tagWarn + " finish or cancel this turn before /connect"), nil
	}
	items := make([]overlayItem, 0, len(m.providers)+1)
	for _, p := range m.providers {
		items = append(items, overlayItem{id: p.Name, title: p.Name, detail: p.Detail})
	}
	items = append(items, overlayItem{id: connectCustom, title: "custom endpoint", detail: "any OpenAI-compatible base URL"})
	ov := overlay{kind: overlayConnect, title: "Connect a provider", items: items}
	m.ov = &ov
	return m, nil
}

// startConnect begins the wizard for the picked provider row.
func (m ChatModel) startConnect(id string) (tea.Model, tea.Cmd) {
	m.connGen++
	c := connectState{provider: id, step: connectStepKey, gen: m.connGen}
	if id == connectCustom {
		c.provider, c.step = "", connectStepURL
	}
	for _, p := range m.providers {
		if p.Name == id {
			c.oauth, c.risk, c.keyURL = p.OAuth, p.Risk, p.KeyURL
		}
	}
	if c.oauth {
		if c.risk == "" {
			return m.connectSignIn(c)
		}
		c.step = connectStepRisk
		m.conn = &c
		return m.append(tagWarn + " " + c.risk).layout(), nil
	}
	m.conn = &c
	return m.layout(), nil
}

// connectSignIn starts the provider's browser sign-in on a background
// goroutine; progress lines stream into the scrollback and esc cancels the
// flow's context.
func (m ChatModel) connectSignIn(c connectState) (tea.Model, tea.Cmd) {
	if m.loginFn == nil {
		m.conn = nil
		return m.append(tagWarn + " sign-in is not available in this session"), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.step, c.busy, c.cancel = connectStepLogin, "finish sign-in in your browser", cancel
	m.conn = &c
	ch := make(chan tea.Msg, 8)
	login, provider, gen := m.loginFn, c.provider, c.gen
	go func() {
		err := login(ctx, provider, func(line string) {
			ch <- connectNoteMsg{gen: gen, line: line, ch: ch}
		})
		ch <- connectLoginMsg{gen: gen, err: err}
		close(ch)
	}()
	return m.layout(), listenConn(ch)
}

// connectKey drives the wizard: enter commits the step, esc cancels,
// backspace edits, ctrl+u clears; typed and pasted runes append.
func (m ChatModel) connectKey(v tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := *m.conn
	switch v.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		return m.connectCancel(c)
	case tea.KeyEnter:
		return m.connectSubmit(c)
	case tea.KeyBackspace:
		if len(c.buf) > 0 {
			c.buf = c.buf[:len(c.buf)-1]
		}
	case tea.KeyCtrlU:
		zeroRunes(c.buf)
		c.buf = nil
	case tea.KeyRunes:
		if c.typing() {
			c.buf = append(slices.Clip(c.buf), v.Runes...)
		}
	case tea.KeySpace:
		if c.typing() {
			c.buf = append(slices.Clip(c.buf), ' ')
		}
	}
	m.conn = &c
	return m, nil
}

// connectCancel ends the wizard, stopping a running sign-in and zeroing
// the secret buffers.
func (m ChatModel) connectCancel(c connectState) (tea.Model, tea.Cmd) {
	if c.cancel != nil {
		c.cancel()
	}
	zeroRunes(c.key)
	zeroRunes(c.buf)
	m.conn = nil
	return m.append(tagWarn + " connect cancelled").layout(), nil
}

// connectSubmit commits the current step.
func (m ChatModel) connectSubmit(c connectState) (tea.Model, tea.Cmd) {
	val := strings.TrimSpace(string(c.buf))
	switch c.step {
	case connectStepURL:
		u, err := url.Parse(val)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return m.append(tagWarn + " base URL must be http(s)://host[/path]"), nil
		}
		c.baseURL, c.step, c.buf = val, connectStepKey, nil
	case connectStepKey:
		if val == "" {
			return m, nil
		}
		c.key = []rune(val)
		zeroRunes(c.buf)
		c.buf = nil
		return m.connectFetch(c)
	case connectStepRisk:
		return m.connectSignIn(c)
	case connectStepModel:
		if val == "" {
			return m, nil
		}
		c.model = val
		return m.connectFinish(c)
	default:
		return m, nil // sign-in or fetch in flight: only esc acts
	}
	m.conn = &c
	return m.layout(), nil
}

// connectFetch validates the credential by fetching the provider's model
// catalog with it on a background goroutine. Without the callback the
// wizard degrades to the typed-id step.
func (m ChatModel) connectFetch(c connectState) (tea.Model, tea.Cmd) {
	if m.connectModelsFn == nil {
		c.step = connectStepModel
		m.conn = &c
		return m.layout(), nil
	}
	c.step, c.busy = connectStepFetch, "checking the credential against the live model list"
	m.conn = &c
	fn, gen := m.connectModelsFn, c.gen
	req := ConnectRequest{Provider: c.provider, BaseURL: c.baseURL, Key: string(c.key)}
	return m.layout(), func() tea.Msg {
		models, note := fn(req)
		return connectModelsMsg{gen: gen, models: models, note: note}
	}
}

// connectModelsDone opens the model picker over what the fetch returned,
// or falls back to the typed-id step when nothing came back.
func (m ChatModel) connectModelsDone(v connectModelsMsg) (tea.Model, tea.Cmd) {
	if m.conn == nil || m.conn.gen != v.gen {
		return m, nil // wizard cancelled while the fetch ran
	}
	c := *m.conn
	c.busy = ""
	if v.note != "" {
		m = m.append(tagWarn + " " + clip(v.note))
	}
	c.step = connectStepModel
	m.conn = &c
	if len(v.models) == 0 {
		return m.layout(), nil
	}
	items := make([]overlayItem, 0, len(v.models)+1)
	for _, id := range v.models {
		items = append(items, overlayItem{id: id, title: id})
	}
	items = append(items, overlayItem{id: connectManual, title: "type a model id", detail: "use one not in this list"})
	ov := overlay{kind: overlayConnectModel, title: "Pick a model — " + connectWho(c), items: items}
	m.ov = &ov
	return m.layout(), nil
}

// connectModelPicked commits the model overlay's selection.
func (m ChatModel) connectModelPicked(id string) (tea.Model, tea.Cmd) {
	if m.conn == nil {
		return m, nil
	}
	c := *m.conn
	if id == connectManual {
		c.step = connectStepModel
		m.conn = &c
		return m.layout(), nil
	}
	c.model = id
	return m.connectFinish(c)
}

// connectLoginDone ends the sign-in step: a success moves on to the
// catalog fetch with the stored token, a failure closes the wizard.
func (m ChatModel) connectLoginDone(v connectLoginMsg) (tea.Model, tea.Cmd) {
	if m.conn == nil || m.conn.gen != v.gen {
		return m, nil
	}
	c := *m.conn
	c.busy, c.cancel = "", nil
	if v.err != nil {
		m.conn = nil
		return m.append(tagWarn + " sign-in failed: " + clip(v.err.Error())).layout(), nil
	}
	m = m.append(tagModel + " signed in to " + c.provider)
	return m.connectFetch(c)
}

// connectFinish hands the collected request to the Connect callback and
// switches onto the returned route. The key is zeroed either way — a
// failure here is a config or store write, not a typo worth retyping.
func (m ChatModel) connectFinish(c connectState) (tea.Model, tea.Cmd) {
	req := ConnectRequest{Provider: c.provider, BaseURL: c.baseURL, Model: c.model, Key: string(c.key)}
	zeroRunes(c.key)
	zeroRunes(c.buf)
	m.conn = nil
	rt, err := m.connectFn(req)
	if err != nil {
		return m.append(tagWarn + " connect failed: " + clip(err.Error())).layout(), nil
	}
	m.routes = append(slices.Clip(m.routes), rt)
	m.active, m.route = rt.Name, rt.Provider+"/"+rt.Model
	return m.append(tagModel + " connected " + rt.Provider + " — route " + rt.Name + " active").layout(), nil
}

// connectWho names the wizard's target for prompts and titles.
func connectWho(c connectState) string {
	if c.provider == "" {
		return "custom"
	}
	return c.provider
}

// connectHint is the status-line help for the wizard's current step.
func connectHint(c connectState) string {
	switch c.step {
	case connectStepKey:
		if c.keyURL != "" {
			return "paste your api key · get one at " + c.keyURL + " · esc cancels"
		}
		return "paste your api key · enter continues · esc cancels"
	case connectStepRisk:
		return "enter accepts the risk · esc cancels"
	case connectStepLogin, connectStepFetch:
		return "working · esc cancels"
	}
	return "enter continues · esc cancels"
}

// connectPromptView is the wizard's one-line prompt, rendered in place of
// the textarea. The key field shows bullets — the secret is never echoed.
func (m ChatModel) connectPromptView() string {
	c := m.conn
	avail := max(1, m.width-4)
	var label, val string
	switch c.step {
	case connectStepURL:
		label, val = "base URL", string(c.buf)
	case connectStepModel:
		label, val = "model id", string(c.buf)
	case connectStepKey:
		label, val = "api key", strings.Repeat("•", min(len(c.buf), 24))
	case connectStepRisk:
		return m.cstyle.warn.Render(fit("connect "+connectWho(*c)+" · enter accepts the risk above · esc cancels", avail))
	default:
		return m.cstyle.dimText(fit(m.sp.View()+" "+c.busy+"… · esc cancels", avail))
	}
	prefix := "connect " + connectWho(*c) + " · " + label + " › "
	room := max(8, avail-len([]rune(prefix))-1)
	return m.cstyle.dimText(prefix) + tailRunes(val, room) + m.cstyle.glyph("▍", m.cstyle.accent)
}

// tailRunes keeps the last n runes of s, marking a cut with an ellipsis, so
// the live end of a long field stays visible while typing.
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n+1:])
}

// zeroRunes best-effort clears a secret buffer before it is dropped.
func zeroRunes(r []rune) {
	for i := range r {
		r[i] = 0
	}
}
