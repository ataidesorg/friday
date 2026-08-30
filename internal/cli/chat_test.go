package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ataidesorg/ink/internal/commands"
	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/redact"
	"github.com/ataidesorg/ink/internal/runtime"
	sessionstore "github.com/ataidesorg/ink/internal/session"
	"github.com/ataidesorg/ink/internal/tools"
	"github.com/ataidesorg/ink/internal/tui"
)

type nopObs struct{}

func (nopObs) OnEvent(core.Event)  {}
func (nopObs) OnPhase(core.Phase)  {}
func (nopObs) OnModelDelta(string) {}

func newTestStore(t *testing.T) *sessionstore.Store {
	t.Helper()
	s, err := sessionstore.NewStore(t.TempDir(), redact.New())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// turn must load prior turns BEFORE appending the current prompt, so the
// history handed to the runtime never includes the prompt (phases.go appends
// Task.Description itself).
func TestChatTurnHistoryExcludesCurrentPrompt(t *testing.T) {
	store := newTestStore(t)
	now := time.Unix(0, 0).UTC()
	if _, err := store.Create("s1", now); err != nil {
		t.Fatal(err)
	}
	for _, tn := range []sessionstore.Turn{
		{Role: core.RoleUser, Text: "old-q", TS: now},
		{Role: core.RoleAssistant, Text: "old-a", TS: now},
	} {
		if _, err := store.Append("s1", tn, now); err != nil {
			t.Fatal(err)
		}
	}

	var sawHistory []core.Message
	var sawTask string
	cs := &chatSession{
		store: store, id: "s1", model: "m1", clock: func() time.Time { return now },
		profile:   core.NewProfileID(),
		sess:      core.SessionID("s1"),
		principal: core.Principal{Kind: core.PrincipalUser, Name: "tester"},
		run: func(_ context.Context, _ runtime.Deps, in runtime.Input) (runtime.Result, error) {
			sawHistory, sawTask = in.History, in.Task.Description
			return runtime.Result{Summary: "reply", Usage: core.Usage{InputTokens: 3, OutputTokens: 4}, Outcome: core.Outcome{Kind: core.OutcomeCompletedVerified}}, nil
		},
	}
	res, err := cs.turn(context.Background(), "new-q", nopObs{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "reply" {
		t.Fatalf("summary = %q", res.Summary)
	}
	if len(sawHistory) != 2 || sawHistory[0].Content != "old-q" || sawHistory[1].Content != "old-a" {
		t.Fatalf("history = %+v, want [old-q old-a] and no current prompt", sawHistory)
	}
	if sawTask != "new-q" {
		t.Fatalf("task description = %q, want new-q", sawTask)
	}
	// Transcript now holds both prior turns plus the user prompt and the reply.
	_, turns, err := store.Load("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 4 {
		t.Fatalf("stored %d turns, want 4: %+v", len(turns), turns)
	}
	if turns[2].Role != core.RoleUser || turns[2].Text != "new-q" {
		t.Fatalf("turn[2] = %+v, want user new-q", turns[2])
	}
	if turns[3].Role != core.RoleAssistant || turns[3].Text != "reply" || turns[3].Model != "m1" {
		t.Fatalf("turn[3] = %+v, want assistant reply model m1", turns[3])
	}
}

func TestChatTurnLoadsSessionGoal(t *testing.T) {
	store := newTestStore(t)
	now := time.Unix(0, 0).UTC()
	if _, err := store.Create("s1", now); err != nil {
		t.Fatal(err)
	}
	g, err := core.NewGoal("ship it", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveGoal("s1", g); err != nil {
		t.Fatal(err)
	}
	var saw *core.Goal
	cs := &chatSession{
		store: store, id: "s1", model: "m1", clock: func() time.Time { return now },
		profile:   core.NewProfileID(),
		sess:      core.SessionID("s1"),
		principal: core.Principal{Kind: core.PrincipalUser, Name: "tester"},
		run: func(_ context.Context, _ runtime.Deps, in runtime.Input) (runtime.Result, error) {
			saw = in.Goal
			return runtime.Result{Summary: "working", Goal: in.Goal, ContinueGoal: true}, nil
		},
	}
	if _, err := cs.turn(context.Background(), "go", nopObs{}); err != nil {
		t.Fatal(err)
	}
	if saw == nil || saw.ID != g.ID || saw.Objective != "ship it" {
		t.Fatalf("turn did not load the session goal: %+v", saw)
	}
}

// A successful turn appends one metrics line carrying the turn's accounting:
// session, model, active route, tool-call count (observed from events), and
// outcome. Metrics are local analytics, separate from the transcript.
func TestChatTurnRecordsMetric(t *testing.T) {
	store := newTestStore(t)
	home := t.TempDir()
	ml, err := sessionstore.NewMetricsLog(home, redact.New())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(0, 0).UTC()
	if _, err := store.Create("s1", now); err != nil {
		t.Fatal(err)
	}
	cs := &chatSession{
		store: store, id: "s1", model: "m1", routeName: "fast", metrics: ml,
		clock:     func() time.Time { return now },
		profile:   core.NewProfileID(),
		sess:      core.SessionID("s1"),
		principal: core.Principal{Kind: core.PrincipalUser, Name: "tester"},
		run: func(_ context.Context, deps runtime.Deps, _ runtime.Input) (runtime.Result, error) {
			// One completed tool call, so the metric's ToolCalls tally is 1.
			deps.Observer.OnEvent(core.NewEvent(core.NewTaskID(), core.NewRunID(), 1, now,
				core.ToolCompleted{Tool: "read_file", Success: true}))
			cost := core.USDMicros(4200)
			return runtime.Result{
				Outcome: core.Outcome{Kind: core.OutcomeCompletedVerified},
				Usage:   core.Usage{InputTokens: 7, OutputTokens: 9},
				Cost:    core.CostReport{Actual: &cost},
			}, nil
		},
	}
	if _, err := cs.turn(context.Background(), "q", nopObs{}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(home, "logs", "metrics.jsonl")) //nolint:gosec // test reads its own temp dir
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("wrote %d metric lines, want 1:\n%s", len(lines), data)
	}
	var m sessionstore.Metric
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("parse metric: %v", err)
	}
	if m.Session != "s1" || m.Model != "m1" || m.Route != "fast" {
		t.Fatalf("metric ids = %+v", m)
	}
	if m.ToolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1", m.ToolCalls)
	}
	if m.Outcome != "completed_verified" {
		t.Fatalf("outcome = %q, want completed_verified", m.Outcome)
	}
	if m.Usage.OutputTokens != 9 {
		t.Fatalf("usage = %+v", m.Usage)
	}
}

// A user-cancelled turn writes no metric: there is no meaningful outcome to
// record, and the run error is context.Canceled.
func TestChatCancelledTurnRecordsNoMetric(t *testing.T) {
	store := newTestStore(t)
	home := t.TempDir()
	ml, err := sessionstore.NewMetricsLog(home, redact.New())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(0, 0).UTC()
	if _, err := store.Create("s1", now); err != nil {
		t.Fatal(err)
	}
	cs := &chatSession{
		store: store, id: "s1", model: "m1", metrics: ml,
		clock:     func() time.Time { return now },
		profile:   core.NewProfileID(),
		sess:      core.SessionID("s1"),
		principal: core.Principal{Kind: core.PrincipalUser, Name: "tester"},
		run: func(context.Context, runtime.Deps, runtime.Input) (runtime.Result, error) {
			return runtime.Result{}, context.Canceled
		},
	}
	if _, err := cs.turn(context.Background(), "q", nopObs{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("turn err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(home, "logs", "metrics.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("cancelled turn wrote a metrics file: stat err = %v", err)
	}
	_, turns, lerr := store.Load("s1")
	if lerr != nil {
		t.Fatalf("Load: %v", lerr)
	}
	if len(turns) != 2 || turns[0].Role != core.RoleUser || turns[0].Text != "q" ||
		turns[1].Role != core.RoleAssistant || !strings.Contains(turns[1].Text, "cancelled") {
		t.Fatalf("cancelled turn context = %+v", turns)
	}
}

func TestWaitTimeout(t *testing.T) {
	// An already-done group returns true well within the deadline.
	var done sync.WaitGroup
	if !waitTimeout(&done, time.Second) {
		t.Fatal("waitTimeout on an empty group must return true")
	}
	// A group that never completes trips the timeout and returns false.
	var stuck sync.WaitGroup
	stuck.Add(1)
	if waitTimeout(&stuck, 20*time.Millisecond) {
		t.Fatal("waitTimeout must return false when the group never finishes")
	}
	stuck.Done() // release the goroutine waitTimeout left blocked on Wait
}

func TestPickSession(t *testing.T) {
	now := time.Unix(0, 0).UTC()

	// --continue a missing session is an error.
	store := newTestStore(t)
	if _, err := pickSession(store, chatFlags{continueID: "nope"}, io.Discard); err == nil {
		t.Fatal("want error resuming missing session")
	}

	// --continue an existing session returns it.
	if _, err := store.Create("existing", now); err != nil {
		t.Fatal(err)
	}
	if id, err := pickSession(store, chatFlags{continueID: "existing"}, io.Discard); err != nil || id != "existing" {
		t.Fatalf("continue: id=%q err=%v", id, err)
	}

	// No flags always starts a fresh session — nothing auto-resumes.
	fresh := newTestStore(t)
	first, err := pickSession(fresh, chatFlags{}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fresh.Load(first); err != nil {
		t.Fatalf("fresh session %q not persisted: %v", first, err)
	}
	second, err := pickSession(fresh, chatFlags{}, io.Discard)
	if err != nil || second == first {
		t.Fatalf("second launch: got=%q err=%v, want a new session, not %q", second, err, first)
	}

	// --resume over nothing but empty shells falls back to a fresh session.
	if got, err := pickSession(fresh, chatFlags{resume: true}, io.Discard); err != nil || got == first || got == second {
		t.Fatalf("--resume with no turns anywhere: got=%q err=%v", got, err)
	}

	// --resume picks the session that actually has turns, skipping shells.
	if _, err := fresh.Append(first, sessionstore.Turn{Role: core.RoleUser, Text: "hi", TS: now}, now); err != nil {
		t.Fatal(err)
	}
	var note strings.Builder
	if got, err := pickSession(fresh, chatFlags{resume: true}, &note); err != nil || got != first {
		t.Fatalf("--resume: got=%q want=%q err=%v", got, first, err)
	}
	if !strings.Contains(note.String(), first) {
		t.Fatalf("resume note missing session id: %q", note.String())
	}
}

// A failed run must persist no turn (no orphan user prompt to desync resume)
// and its error must be redacted before it can reach the TUI.
func TestChatTurnFailureLeavesNoOrphanAndRedacts(t *testing.T) {
	store := newTestStore(t)
	now := time.Unix(0, 0).UTC()
	if _, err := store.Create("s1", now); err != nil {
		t.Fatal(err)
	}
	cs := &chatSession{
		store: store, id: "s1", model: "m1", clock: func() time.Time { return now },
		profile:   core.NewProfileID(),
		sess:      core.SessionID("s1"),
		principal: core.Principal{Kind: core.PrincipalUser, Name: "tester"},
		red:       redact.New("SECRET-XYZ"),
		run: func(_ context.Context, _ runtime.Deps, _ runtime.Input) (runtime.Result, error) {
			return runtime.Result{}, errors.New("provider blew up: token SECRET-XYZ rejected")
		},
	}
	_, err := cs.turn(context.Background(), "new-q", nopObs{})
	if err == nil {
		t.Fatal("turn must surface the run error")
	}
	if strings.Contains(err.Error(), "SECRET-XYZ") {
		t.Fatalf("error not redacted: %q", err.Error())
	}
	_, turns, lerr := store.Load("s1")
	if lerr != nil {
		t.Fatalf("Load: %v", lerr)
	}
	if len(turns) != 0 {
		t.Fatalf("failed turn left %d orphan turns: %+v", len(turns), turns)
	}
}

func TestSetAgent(t *testing.T) {
	reg := tools.Default(nil, nil)
	cs := &chatSession{
		deps: runtime.Deps{Tools: reg},
		cfg: config.Config{Agents: map[string]config.AgentConfig{
			"reader": {Prompt: "Only read.", Tools: []string{"read_file", "list_dir"}},
		}},
	}
	if err := cs.setAgent("reader"); err != nil {
		t.Fatal(err)
	}
	if cs.base.AgentPrompt != "Only read." || cs.agent != "reader" {
		t.Fatalf("prompt=%q agent=%q", cs.base.AgentPrompt, cs.agent)
	}
	if _, ok := cs.deps.Tools.Get("write_file"); ok {
		t.Fatal("allowlist must drop write_file")
	}
	if _, ok := cs.deps.Tools.Get("read_file"); !ok {
		t.Fatal("allowlist must keep read_file")
	}
	if err := cs.setAgent(""); err != nil {
		t.Fatal(err)
	}
	if cs.base.AgentPrompt != "" || cs.agent != "" {
		t.Fatalf("reset left prompt=%q agent=%q", cs.base.AgentPrompt, cs.agent)
	}
	if _, ok := cs.deps.Tools.Get("write_file"); !ok {
		t.Fatal("reset must restore the full registry")
	}
	if err := cs.setAgent("nope"); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

// resumeLatest repoints the chat at the newest prior session that has turns,
// skipping empty shells and the current session, and drops summary turns
// from the replayed transcript.
func TestChatSessionResumeLatest(t *testing.T) {
	store := newTestStore(t)
	now := time.Unix(0, 0).UTC()
	for i, id := range []string{"prev", "cur", "shell"} {
		if _, err := store.Create(id, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tn := range []sessionstore.Turn{
		{Role: core.RoleSystem, Kind: sessionstore.KindSummary, Text: "folded", TS: now},
		{Role: core.RoleUser, Text: "hi", TS: now},
		{Role: core.RoleAssistant, Text: "hello", TS: now},
	} {
		if _, err := store.Append("prev", tn, now); err != nil {
			t.Fatal(err)
		}
	}

	cs := &chatSession{store: store, id: "cur"}
	id, turns, err := cs.resumeLatest()
	if err != nil {
		t.Fatal(err)
	}
	if id != "prev" || cs.id != "prev" || cs.sess != core.SessionID("prev") {
		t.Fatalf("resumed id=%q cs.id=%q cs.sess=%q, want prev", id, cs.id, cs.sess)
	}
	if len(turns) != 2 || turns[0].Role != "user" || turns[0].Text != "hi" ||
		turns[1].Role != "assistant" || turns[1].Text != "hello" {
		t.Fatalf("replay turns %+v, want the user/assistant pair without the summary", turns)
	}

	// Nothing behind the current session: an explicit error, not a reset.
	lone := newTestStore(t)
	if _, err := lone.Create("only", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&chatSession{store: lone, id: "only"}).resumeLatest(); err == nil {
		t.Fatal("resumeLatest with no prior turns must error")
	}
}

func TestChatSessionResumeByID(t *testing.T) {
	store := newTestStore(t)
	now := time.Unix(0, 0).UTC()
	for _, id := range []string{"old", "cur"} {
		if _, err := store.Create(id, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Append("old", sessionstore.Turn{Role: core.RoleUser, Text: "from old", TS: now}, now); err != nil {
		t.Fatal(err)
	}
	cs := &chatSession{store: store, id: "cur"}
	infos := cs.sessionInfos()
	if len(infos) != 1 || infos[0].ID != "old" {
		t.Fatalf("sessionInfos = %+v, want old only", infos)
	}
	if infos[0].Title != "from old" {
		t.Fatalf("session title = %q, want first prompt", infos[0].Title)
	}
	id, turns, err := cs.resumeByID("old")
	if err != nil || id != "old" || len(turns) != 1 || turns[0].Text != "from old" {
		t.Fatalf("resumeByID: id=%q turns=%+v err=%v", id, turns, err)
	}
	if _, _, err := cs.resumeByID("cur"); err == nil {
		t.Fatal("resumeByID must refuse the live session")
	}
}

func TestChatSessionSetMode(t *testing.T) {
	store := newTestStore(t)
	now := time.Unix(0, 0).UTC()
	if _, err := store.Create("s1", now); err != nil {
		t.Fatal(err)
	}
	reg := tools.Default(nil, nil)
	cs := &chatSession{
		store: store, id: "s1", deps: runtime.Deps{Tools: reg}, baseTools: reg,
		clock: func() time.Time { return now }, cfg: config.Config{},
	}
	if err := cs.setMode("plan"); err != nil {
		t.Fatal(err)
	}
	if _, ok := cs.deps.Tools.Get("write_file"); ok {
		t.Fatal("plan mode kept write_file")
	}
	if _, ok := cs.deps.Tools.Get("read_file"); !ok {
		t.Fatal("plan mode dropped read_file")
	}
	meta, err := store.Meta("s1")
	if err != nil || meta.Mode != "plan" {
		t.Fatalf("persisted mode = %+v err=%v", meta, err)
	}
	if err := cs.setMode("code"); err != nil {
		t.Fatal(err)
	}
	if _, ok := cs.deps.Tools.Get("write_file"); !ok {
		t.Fatal("code mode did not restore write_file")
	}
}

func TestChatSessionRewindForkRename(t *testing.T) {
	store := newTestStore(t)
	now := time.Unix(0, 0).UTC()
	if _, err := store.Create("s1", now); err != nil {
		t.Fatal(err)
	}
	for _, tn := range []sessionstore.Turn{
		{Role: core.RoleUser, Text: "one", TS: now},
		{Role: core.RoleAssistant, Text: "a1", TS: now},
		{Role: core.RoleUser, Text: "two", TS: now},
		{Role: core.RoleAssistant, Text: "a2", TS: now},
	} {
		if _, err := store.Append("s1", tn, now); err != nil {
			t.Fatal(err)
		}
	}
	cs := &chatSession{store: store, id: "s1", clock: func() time.Time { return now }}
	if err := cs.rename("login fix"); err != nil {
		t.Fatal(err)
	}
	if meta, err := store.Meta("s1"); err != nil || meta.Title != "login fix" {
		t.Fatalf("title = %+v err %v", meta, err)
	}
	if err := cs.rewind(1); err != nil {
		t.Fatal(err)
	}
	_, turns, err := store.Load("s1")
	if err != nil || len(turns) != 2 || turns[0].Text != "one" {
		t.Fatalf("rewound = %+v err %v", turns, err)
	}
	id, err := cs.fork()
	if err != nil || id == "" || id == "s1" {
		t.Fatalf("fork id=%q err=%v", id, err)
	}
	if cs.id != id {
		t.Fatalf("session still on %s, want %s", cs.id, id)
	}
	_, ft, err := store.Load(id)
	if err != nil || len(ft) != 2 {
		t.Fatalf("fork turns = %+v err %v", ft, err)
	}
}

func TestChatSessionTodosAndAgents(t *testing.T) {
	store := newTestStore(t)
	now := time.Unix(0, 0).UTC()
	if _, err := store.Create("s1", now); err != nil {
		t.Fatal(err)
	}
	cs := &chatSession{store: store, id: "s1", clock: func() time.Time { return now }}
	if err := cs.saveTodos([]tools.TodoItem{{ID: "1", Content: "dash", Status: tools.TodoInProgress}}); err != nil {
		t.Fatal(err)
	}
	got := cs.todosForTUI()
	if len(got) != 1 || got[0].Content != "dash" {
		t.Fatalf("tui todos = %+v", got)
	}
	id, err := cs.createAgent()
	if err != nil || id == "" || id == "s1" {
		t.Fatalf("createAgent %q err %v", id, err)
	}
	rows := cs.listAgents()
	if len(rows) != 2 {
		t.Fatalf("list = %+v", rows)
	}
	if err := cs.deleteAgent(id); err != nil {
		t.Fatal(err)
	}
	if rows := cs.listAgents(); len(rows) != 1 || rows[0].ID != "s1" {
		t.Fatalf("after delete %+v", rows)
	}
}

func TestEmptySessionIsNotKept(t *testing.T) {
	store := newTestStore(t)
	now := time.Unix(0, 0).UTC()
	if _, err := store.Create("empty", now); err != nil {
		t.Fatal(err)
	}
	cs := &chatSession{store: store, id: "empty", clock: func() time.Time { return now }}
	cs.dropIfEmpty(io.Discard)
	if _, err := store.Meta("empty"); err == nil {
		t.Fatal("empty session should have been deleted")
	}

	if _, err := store.Create("used", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("used", sessionstore.Turn{Role: core.RoleUser, Text: "hi", TS: now}, now); err != nil {
		t.Fatal(err)
	}
	cs = &chatSession{store: store, id: "used", clock: func() time.Time { return now }}
	cs.dropIfEmpty(io.Discard)
	if _, err := store.Meta("used"); err != nil {
		t.Fatalf("session with a turn was deleted: %v", err)
	}
}

func TestRotateDropsEmptyPrevious(t *testing.T) {
	store := newTestStore(t)
	now := time.Unix(0, 0).UTC()
	if _, err := store.Create("old", now); err != nil {
		t.Fatal(err)
	}
	cs := &chatSession{store: store, id: "old", clock: func() time.Time { return now }}
	id, err := cs.rotate()
	if err != nil || id == "" || id == "old" {
		t.Fatalf("rotate id=%q err=%v", id, err)
	}
	if _, err := store.Meta("old"); err == nil {
		t.Fatal("empty previous session should have been deleted")
	}
	if _, err := store.Meta(id); err != nil {
		t.Fatalf("new session missing: %v", err)
	}

	if _, err := store.Append(id, sessionstore.Turn{Role: core.RoleUser, Text: "hi", TS: now}, now); err != nil {
		t.Fatal(err)
	}
	cs.id = id
	next, err := cs.rotate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Meta(id); err != nil {
		t.Fatalf("session with a turn was deleted on /new: %v", err)
	}
	if next == id {
		t.Fatal("rotate returned the same id")
	}
}

func TestReservedSlashComesFromTheTUITable(t *testing.T) {
	got := reservedSlash()
	names := tui.SlashNames()
	if len(got) != len(names) {
		t.Fatalf("reserved %d, SlashNames %d", len(got), len(names))
	}
	for _, name := range names {
		if !got[name] {
			t.Fatalf("reservedSlash missing %q", name)
		}
	}
	if got["cycle-mode"] {
		t.Fatal("palette-only cycle-mode must not be reserved")
	}

	root := t.TempDir()
	dir := filepath.Join(root, ".ink", "commands")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "help.md"), []byte("must not load\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "greet.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := commands.Load(root, "", io.Discard, got)
	if len(loaded) != 1 || loaded[0].Name != "greet" {
		t.Fatalf("loaded %+v, help.md must not shadow /help", loaded)
	}
}
