package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/redact"
)

func newStore(t *testing.T, literals ...string) (*Store, string) {
	t.Helper()
	home := t.TempDir()
	s, err := NewStore(home, redact.New(literals...))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, home
}

func at(sec int) time.Time { return time.Date(2026, 8, 24, 12, 0, sec, 0, time.UTC) }

func TestNewStoreFailsClosed(t *testing.T) {
	if _, err := NewStore("/home", nil); err == nil {
		t.Fatal("nil redactor must be rejected")
	}
	if _, err := NewStore("", redact.New()); err == nil {
		t.Fatal("empty home must be rejected")
	}
}

func TestCreateAppendLoadRoundTrip(t *testing.T) {
	s, _ := newStore(t)
	m, err := s.Create("sess-1", at(0))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if m.Turns != 0 || !m.Created.Equal(at(0)) {
		t.Fatalf("fresh meta = %+v", m)
	}

	in := []Turn{
		{Role: core.RoleUser, Text: "hello", TS: at(1)},
		{Role: core.RoleAssistant, Text: "hi there", Model: "kimi", TS: at(2)},
	}
	var last Meta
	for i, tn := range in {
		last, err = s.Append("sess-1", tn, at(3+i))
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if last.Turns != 2 {
		t.Fatalf("Turns = %d, want 2", last.Turns)
	}
	if !last.Updated.Equal(at(4)) {
		t.Fatalf("Updated = %v, want %v", last.Updated, at(4))
	}

	gotMeta, turns, err := s.Load("sess-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if gotMeta.Turns != 2 {
		t.Fatalf("loaded meta Turns = %d", gotMeta.Turns)
	}
	if len(turns) != 2 || turns[0].Text != "hello" || turns[1].Model != "kimi" {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[0].Role != core.RoleUser || turns[1].Role != core.RoleAssistant {
		t.Fatalf("roles = %q, %q", turns[0].Role, turns[1].Role)
	}
}

func TestAppendRedactsAndCallerTurnUntouched(t *testing.T) {
	secret := "TOP" + "SECRET" + "VALUE" + "42"
	s, home := newStore(t, secret)
	if _, err := s.Create("s", at(0)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	orig := Turn{Role: core.RoleUser, Text: "my key is " + secret + " ok", TS: at(1)}
	if _, err := s.Append("s", orig, at(2)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if orig.Text != "my key is "+secret+" ok" {
		t.Fatal("Append mutated the caller's Turn")
	}

	raw, err := os.ReadFile(filepath.Join(home, "sessions", "s", transcriptFile)) //nolint:gosec // test reads a transcript under a temp home
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("transcript leaks the secret:\n%s", raw)
	}
	if redact.New(secret).ContainsSecret(string(raw)) {
		t.Fatal("redactor still finds the secret on disk")
	}

	_, turns, err := s.Load("s")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(turns) != 1 || strings.Contains(turns[0].Text, secret) {
		t.Fatalf("loaded turn still carries the secret: %+v", turns)
	}
}

func TestTranscriptFileMode0600(t *testing.T) {
	s, home := newStore(t)
	if _, err := s.Create("s", at(0)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Append("s", Turn{Role: core.RoleUser, Text: "x", TS: at(1)}, at(2)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	for _, name := range []string{transcriptFile, metaFile} {
		info, err := os.Stat(filepath.Join(home, "sessions", "s", name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != filePerm {
			t.Fatalf("%s mode = %o, want %o", name, perm, filePerm)
		}
	}
}

func TestListSortsNewestFirstAndLatest(t *testing.T) {
	s, _ := newStore(t)
	// Create out of update order: b updated last.
	if _, err := s.Create("a", at(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("b", at(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append("a", Turn{Role: core.RoleUser, Text: "1", TS: at(2)}, at(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append("b", Turn{Role: core.RoleUser, Text: "1", TS: at(9)}, at(9)); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].ID != "b" || list[1].ID != "a" {
		t.Fatalf("List order = %v", list)
	}
	latest, ok, err := s.Latest()
	if err != nil || !ok || latest.ID != "b" {
		t.Fatalf("Latest = %v ok=%v err=%v", latest, ok, err)
	}
}

func TestListEmptyAndLoadUnknown(t *testing.T) {
	s, _ := newStore(t)
	list, err := s.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("empty List = %v err=%v", list, err)
	}
	if _, ok, err := s.Latest(); err != nil || ok {
		t.Fatalf("empty Latest ok=%v err=%v", ok, err)
	}
	if _, _, err := s.Load("nope"); err == nil {
		t.Fatal("Load of unknown session must error")
	}
}

func TestLoadToleratesTruncatedTrailingLine(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Create("s1", at(0)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Append("s1", Turn{Role: core.RoleUser, Text: "hi", TS: at(1)}, at(1)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Simulate a crash mid-Append: a partial JSON line with no newline.
	path := filepath.Join(s.Root(), "s1", transcriptFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(`{"role":"assistant","text":`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, turns, err := s.Load("s1")
	if err != nil {
		t.Fatalf("Load must tolerate a truncated trailing line, got %v", err)
	}
	if len(turns) != 1 || turns[0].Text != "hi" {
		t.Fatalf("turns = %+v, want the one intact turn", turns)
	}
}

func TestLoadRejectsInteriorCorruption(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Create("s1", at(0)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Append("s1", Turn{Role: core.RoleUser, Text: "hi", TS: at(1)}, at(1)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// A bad line followed by a valid one is interior damage, not a torn write.
	path := filepath.Join(s.Root(), "s1", transcriptFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString("garbage\n{\"role\":\"assistant\",\"text\":\"ok\"}\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, _, err := s.Load("s1"); err == nil {
		t.Fatal("Load must reject interior corruption")
	}
}
