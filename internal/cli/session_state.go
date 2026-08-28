package cli

// The interactive chat REPL: bare `friday` on a terminal opens a
// multi-turn conversation that persists to the session store and resumes
// across launches. One heavy graph (provider, sandbox, workspace, policy) is
// built per launch and reused across every turn; each turn runs one
// runtime.Run over the prior transcript as history.

import (
	"errors"
	"fmt"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/runtime"
	sessionstore "github.com/ataidesorg/friday/internal/session"
	"github.com/ataidesorg/friday/internal/skills"
	"github.com/ataidesorg/friday/internal/tools"
	"github.com/ataidesorg/friday/internal/tui"
)

// switchRoute swaps the active route live (TUI /model NAME), persisting the
// choice as a per-session override so the next launch resumes on it. Only the
// provider-coupled deps change; the workspace, sandbox, policy, spend ledger,
// and trail are kept, so the conversation continues uninterrupted. The old
// provider target is closed, which zeroes its credentials. Refused
// by the TUI mid-turn, so no in-flight run observes a half-swapped graph.
func (c *chatSession) switchRoute(name string) error {
	target, err := resolveRoute(c.cfg, name, "", c.red, c.environ)
	if err != nil {
		return errors.New(c.red.Redact(err.Error()))
	}
	if _, err := c.store.SetDefaultRoute(c.id, name, c.clock()); err != nil {
		target.close()
		return errors.New(c.red.Redact(err.Error())) // every error reaching the TUI is redacted
	}
	old := c.target
	c.target = target
	c.deps.Provider = target.provider
	c.deps.Route = &target.decision
	c.base.Model = target.model
	c.model = target.model
	c.routeName = name
	c.histChars = histCharsFor(target.provider)
	old.close()
	return nil
}

// rotate starts a fresh session and points the chat at it, so subsequent
// turns load an empty transcript. The /new command calls this on the UI
// goroutine; the old session stays on disk.
func (c *chatSession) loadTodos() []tools.TodoItem {
	if c.store == nil {
		return nil
	}
	items, err := c.store.LoadTodos(c.sid())
	if err != nil {
		return nil
	}
	out := make([]tools.TodoItem, len(items))
	for i, it := range items {
		out[i] = tools.TodoItem{ID: it.ID, Content: it.Content, Status: it.Status}
	}
	return out
}

func (c *chatSession) saveTodos(items []tools.TodoItem) error {
	if c.store == nil {
		return errors.New("no session store")
	}
	raw := make([]sessionstore.TodoItem, len(items))
	for i, it := range items {
		raw[i] = sessionstore.TodoItem{ID: it.ID, Content: it.Content, Status: it.Status}
	}
	return c.store.SaveTodos(c.sid(), raw)
}

func (c *chatSession) todosForTUI() []tui.TodoItem {
	if c.store == nil {
		return nil
	}
	items, err := c.store.LoadTodos(c.id)
	if err != nil {
		return nil
	}
	out := make([]tui.TodoItem, len(items))
	for i, it := range items {
		out[i] = tui.TodoItem{ID: it.ID, Content: it.Content, Status: it.Status}
	}
	return out
}

func (c *chatSession) listAgents() []tui.DashAgent {
	if c.store == nil {
		return nil
	}
	metas, err := c.store.List()
	if err != nil {
		return nil
	}
	out := make([]tui.DashAgent, 0, len(metas))
	for _, meta := range metas {
		st := "idle"
		if meta.ID == c.id {
			st = "idle"
		}
		peek := ""
		title := meta.Title
		if _, turns, lerr := c.store.Load(meta.ID); lerr == nil {
			title = sessionListTitle(meta, turns)
			for i := len(turns) - 1; i >= 0; i-- {
				if turns[i].Role == core.RoleAssistant && turns[i].Kind != sessionstore.KindSummary {
					peek = turns[i].Text
					break
				}
			}
		}
		out = append(out, tui.DashAgent{
			ID:     meta.ID,
			Title:  title,
			Detail: fmt.Sprintf("%d turns", meta.Turns),
			Peek:   peek,
			State:  st,
		})
	}
	return out
}

func (c *chatSession) createAgent() (string, error) {
	if c.store == nil {
		return "", errors.New("no session store")
	}
	id := string(core.NewSessionID())
	if _, err := c.store.Create(id, c.clock()); err != nil {
		return "", err
	}
	return id, nil
}

func (c *chatSession) attachAgent(id string) (string, []tui.HistoryTurn, error) {
	if c.store == nil {
		return "", nil, errors.New("no session store")
	}
	meta, turns, err := c.store.Load(id)
	if err != nil {
		return "", nil, err
	}
	_, hist, err := c.replay(id, turns)
	return meta.Title, hist, err
}

func (c *chatSession) deleteAgent(id string) error {
	if c.store == nil {
		return errors.New("no session store")
	}
	if id == c.id {
		old := c.id
		if _, err := c.rotate(); err != nil {
			return err
		}
		return c.store.Delete(old)
	}
	return c.store.Delete(id)
}

func (c *chatSession) deleteSession() (string, error) {
	if c.store == nil {
		return "", errors.New("no session store")
	}
	old := c.id
	if _, err := c.rotate(); err != nil {
		return "", err
	}
	if err := c.store.Delete(old); err != nil {
		return "", err
	}
	return c.id, nil
}

func (c *chatSession) rotate() (string, error) {
	id := string(core.NewSessionID())
	if _, err := c.store.Create(id, c.clock()); err != nil {
		return "", err
	}
	c.id, c.sess = id, core.SessionID(id)
	return id, nil
}

func (c *chatSession) rewind(keepUsers int) error {
	if c.store == nil {
		return errors.New("no session store")
	}
	_, turns, err := c.store.Load(c.id)
	if err != nil {
		return err
	}
	kept, users := 0, 0
	for i, tn := range turns {
		if tn.Role == core.RoleUser {
			if users == keepUsers {
				break
			}
			users++
		}
		kept = i + 1
	}
	_, err = c.store.Truncate(c.id, kept, c.clock())
	return err
}

func (c *chatSession) fork() (string, error) {
	if c.store == nil {
		return "", errors.New("no session store")
	}
	id := string(core.NewSessionID())
	if _, err := c.store.Fork(c.id, id, c.clock()); err != nil {
		return "", err
	}
	c.id, c.sess = id, core.SessionID(id)
	return id, nil
}

func (c *chatSession) rename(title string) error {
	if c.store == nil {
		return errors.New("no session store")
	}
	_, err := c.store.SetTitle(c.id, title, c.clock())
	return err
}

// resumeLatest points the chat at the most recent prior session that has
// turns — skipping the current one and the empty shells fresh launches
// leave behind — and returns its id and transcript for replay. The /resume
// command calls this on the UI goroutine; the TUI refuses it mid-turn.
// The resumed session keeps this launch's route; its own saved route
// override applies from the next launch.
func (c *chatSession) resumeLatest() (string, []tui.HistoryTurn, error) {
	metas, err := c.store.List()
	if err != nil {
		return "", nil, err
	}
	for _, m := range metas {
		if m.ID == c.id || m.Turns == 0 {
			continue
		}
		_, turns, err := c.store.Load(m.ID)
		if err != nil {
			return "", nil, err
		}
		return c.replay(m.ID, turns)
	}
	return "", nil, errors.New("no previous session to resume")
}

func (c *chatSession) sessionInfos() []tui.SessionInfo {
	if c.store == nil {
		return nil
	}
	metas, err := c.store.List()
	if err != nil {
		return nil
	}
	out := make([]tui.SessionInfo, 0, len(metas))
	for _, m := range metas {
		if m.ID == c.id || m.Turns == 0 {
			continue
		}
		title := m.Title
		var turns []sessionstore.Turn
		if _, loaded, lerr := c.store.Load(m.ID); lerr == nil {
			turns = loaded
			title = sessionListTitle(m, turns)
		}
		if title == "" {
			title = m.ID
			if len(title) > 8 {
				title = title[:8]
			}
		}
		out = append(out, tui.SessionInfo{
			ID:     m.ID,
			Title:  title,
			Detail: fmt.Sprintf("%d turns", m.Turns),
		})
	}
	return out
}

func (c *chatSession) resumeByID(id string) (string, []tui.HistoryTurn, error) {
	if c.store == nil || id == "" || id == c.id {
		return "", nil, errors.New("no previous session to resume")
	}
	_, turns, err := c.store.Load(id)
	if err != nil {
		return "", nil, err
	}
	if len(turns) == 0 {
		return "", nil, errors.New("no previous session to resume")
	}
	return c.replay(id, turns)
}

func (c *chatSession) replay(id string, turns []sessionstore.Turn) (string, []tui.HistoryTurn, error) {
	c.id, c.sess = id, core.SessionID(id)
	out := make([]tui.HistoryTurn, 0, len(turns))
	for _, t := range turns {
		if t.Kind == sessionstore.KindSummary {
			continue
		}
		out = append(out, tui.HistoryTurn{Role: string(t.Role), Text: t.Text})
	}
	return id, out, nil
}

// addSkills registers the skill tool and advertises the loaded skills to the
// runtime. Called once before the TUI starts, on the launch goroutine.
func (c *chatSession) addSkills(list []skills.Skill) error {
	if len(list) == 0 {
		return nil
	}
	reg, err := c.deps.Tools.With(skills.Tool(list))
	if err != nil {
		return fmt.Errorf("register skill tool: %w", err)
	}
	c.deps.Tools = reg
	c.baseTools = reg // keep the /agent reset snapshot current
	infos := make([]runtime.SkillInfo, len(list))
	for i, s := range list {
		infos[i] = runtime.SkillInfo{Name: s.Name, Description: s.Description}
	}
	c.base.Skills = infos
	c.loadedSkills = list
	return nil
}

func (c *chatSession) skillInfos() []tui.SkillInfo {
	out := make([]tui.SkillInfo, len(c.loadedSkills))
	for i, s := range c.loadedSkills {
		out[i] = tui.SkillInfo{Name: s.Name, Description: s.Description, Path: s.Path, Source: skillSource(s.Path)}
	}
	return out
}

// setAgent activates a named agent profile: its prompt joins the system
// prompt, its tool allowlist narrows the registry, and its route (when set)
// swaps the model. "" resets to the base session. Runs on the UI goroutine
// between turns, like switchRoute.
func (c *chatSession) setAgent(name string) error {
	if c.baseTools == nil {
		c.baseTools = c.deps.Tools
	}
	if name == "" {
		c.base.AgentPrompt = ""
		c.agent = ""
		c.applyTools()
		return nil
	}
	a, ok := c.cfg.Agents[name]
	if !ok {
		return fmt.Errorf("unknown agent %q", name)
	}
	if a.Route != "" && a.Route != c.routeName {
		if err := c.switchRoute(a.Route); err != nil {
			return fmt.Errorf("agent route: %w", err)
		}
	}
	c.base.AgentPrompt = a.Prompt
	c.agent = name
	c.applyTools()
	return nil
}

var readOnlyTools = []string{"read_file", "list_dir", "search", "skill", "ask_user_question", "todo_write"}

func (c *chatSession) setMode(name string) error {
	ok := false
	for _, n := range []string{"code", "plan", "ask"} {
		if name == n {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("unknown mode %q", name)
	}
	if c.baseTools == nil {
		c.baseTools = c.deps.Tools
	}
	c.mode = name
	c.applyTools()
	if c.store == nil {
		return nil
	}
	_, err := c.store.SetMode(c.id, name, c.clock())
	return err
}

func (c *chatSession) applyTools() {
	if c.baseTools == nil {
		c.baseTools = c.deps.Tools
	}
	reg := c.baseTools
	if c.agent != "" {
		if a, ok := c.cfg.Agents[c.agent]; ok {
			reg = reg.Filter(a.Tools)
		}
	}
	if c.mode == "plan" || c.mode == "ask" {
		reg = reg.Filter(readOnlyTools)
	}
	c.deps.Tools = reg
}
