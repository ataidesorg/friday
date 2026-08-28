package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/redact"
)

func TestNewMetricsLogFailsClosed(t *testing.T) {
	if _, err := NewMetricsLog("/home", nil); err == nil {
		t.Fatal("nil redactor must be rejected")
	}
	if _, err := NewMetricsLog("", redact.New()); err == nil {
		t.Fatal("empty home must be rejected")
	}
}

func TestMetricsLogAppendRedactsAndPersists(t *testing.T) {
	home := t.TempDir()
	// A registered literal appearing anywhere in a metric line must not survive.
	log, err := NewMetricsLog(home, redact.New("sk-SECRET"))
	if err != nil {
		t.Fatalf("NewMetricsLog: %v", err)
	}
	if err := log.Append(Metric{Session: "s1", Model: "kimi", Route: "sk-SECRET", ToolCalls: 2, LatencyMS: 5, Outcome: "completed_verified"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := log.Append(Metric{Session: "s1", Model: "kimi", Route: "fast", Usage: core.Usage{InputTokens: 3, OutputTokens: 4}}); err != nil {
		t.Fatalf("Append second: %v", err)
	}

	path := filepath.Join(home, "logs", "metrics.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat metrics: %v", err)
	}
	if perm := info.Mode().Perm(); perm != filePerm {
		t.Fatalf("metrics mode = %v, want %v", perm, os.FileMode(filePerm))
	}

	f, err := os.Open(path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("open metrics: %v", err)
	}
	defer func() { _ = f.Close() }()
	var lines []Metric
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		raw := sc.Text()
		if strings.Contains(raw, "sk-SECRET") {
			t.Fatalf("secret leaked into metrics line: %s", raw)
		}
		var m Metric
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("parse metric line %q: %v", raw, err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 2 {
		t.Fatalf("wrote %d metric lines, want 2", len(lines))
	}
	if lines[0].ToolCalls != 2 || lines[0].LatencyMS != 5 || lines[0].Outcome != "completed_verified" {
		t.Fatalf("line 0 = %+v", lines[0])
	}
	if lines[1].Usage.OutputTokens != 4 {
		t.Fatalf("line 1 usage = %+v", lines[1].Usage)
	}
}

func TestStoreSetDefaultRoute(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Create("s1", at(0)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.SetDefaultRoute("s1", "fast", at(1)); err != nil {
		t.Fatalf("SetDefaultRoute: %v", err)
	}
	m, err := s.Meta("s1")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if m.DefaultRoute != "fast" {
		t.Fatalf("DefaultRoute = %q, want fast", m.DefaultRoute)
	}
	if !m.Updated.Equal(at(1)) {
		t.Fatalf("Updated = %v, want %v", m.Updated, at(1))
	}
	// A missing session is an error, not a silent create.
	if _, err := s.SetDefaultRoute("nope", "fast", at(2)); err == nil {
		t.Fatal("SetDefaultRoute on missing session must error")
	}
}

func TestStoreSetMode(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Create("s1", at(0)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.SetMode("s1", "plan", at(1)); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	m, err := s.Meta("s1")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if m.Mode != "plan" {
		t.Fatalf("Mode = %q, want plan", m.Mode)
	}
	if _, err := s.SetMode("nope", "ask", at(2)); err == nil {
		t.Fatal("SetMode on missing session must error")
	}
}

func TestStoreSetTitleTruncateFork(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Create("s1", at(0)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i, tn := range []Turn{
		{Role: core.RoleUser, Text: "one", TS: at(1)},
		{Role: core.RoleAssistant, Text: "a1", TS: at(2)},
		{Role: core.RoleUser, Text: "two", TS: at(3)},
		{Role: core.RoleAssistant, Text: "a2", TS: at(4)},
	} {
		if _, err := s.Append("s1", tn, at(5+i)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if _, err := s.SetTitle("s1", "login fix", at(10)); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}
	meta, err := s.Meta("s1")
	if err != nil || meta.Title != "login fix" {
		t.Fatalf("title meta = %+v err %v", meta, err)
	}
	if _, err := s.Truncate("s1", 2, at(11)); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	got, turns, err := s.Load("s1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Turns != 2 || len(turns) != 2 || turns[0].Text != "one" || turns[1].Text != "a1" {
		t.Fatalf("truncated = meta%+v turns%+v", got, turns)
	}
	forked, err := s.Fork("s1", "s2", at(12))
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if forked.ID != "s2" || forked.Turns != 2 {
		t.Fatalf("fork meta = %+v", forked)
	}
	_, ft, err := s.Load("s2")
	if err != nil || len(ft) != 2 || ft[1].Text != "a1" {
		t.Fatalf("fork turns = %+v err %v", ft, err)
	}
	if _, err := s.Truncate("s1", 0, at(13)); err != nil {
		t.Fatalf("Truncate empty: %v", err)
	}
	empty, et, err := s.Load("s1")
	if err != nil || empty.Turns != 0 || len(et) != 0 {
		t.Fatalf("empty truncate = meta%+v turns%+v err %v", empty, et, err)
	}
}

func TestStoreTodosAndDelete(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Create("s1", at(0)); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadTodos("s1")
	if err != nil || got != nil {
		t.Fatalf("empty todos = %v err %v", got, err)
	}
	if err := s.SaveTodos("s1", []TodoItem{{ID: "1", Content: "ship", Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadTodos("s1")
	if err != nil || len(got) != 1 || got[0].Content != "ship" {
		t.Fatalf("roundtrip = %+v err %v", got, err)
	}
	if err := s.Delete("s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Meta("s1"); err == nil {
		t.Fatal("deleted session still has meta")
	}
	if err := s.Delete("nope"); err == nil {
		t.Fatal("delete missing must error")
	}
}
