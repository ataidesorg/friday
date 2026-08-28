package cli

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestListModelChoosesOrAborts pins the contract trustPromptTUI leans on:
// enter picks the row under the cursor, and aborting picks nothing, so the
// caller can treat an abort as the safe answer.
func TestListModelChoosesOrAborts(t *testing.T) {
	items := []listItem{{label: "No", note: "safe"}, {label: "Yes, trust it"}}
	named := map[string]tea.KeyType{"enter": tea.KeyEnter, "esc": tea.KeyEsc, "up": tea.KeyUp, "down": tea.KeyDown}
	press := func(m listModel, keys ...string) listModel {
		for _, k := range keys {
			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
			if kt, ok := named[k]; ok {
				msg = tea.KeyMsg{Type: kt}
			}
			next, _ := m.Update(msg)
			m = next.(listModel)
		}
		return m
	}
	base := listModel{title: "Trust this file?", items: items}

	if m := press(base, "enter"); !m.chosen || m.cursor != 0 {
		t.Fatalf("enter on first row: chosen=%v cursor=%d", m.chosen, m.cursor)
	}
	if m := press(base, "down", "enter"); !m.chosen || m.cursor != 1 {
		t.Fatalf("down then enter: chosen=%v cursor=%d", m.chosen, m.cursor)
	}
	for _, key := range []string{"esc", "q"} {
		if m := press(base, key); m.chosen {
			t.Errorf("%s must not choose", key)
		}
	}
	if m := press(base, "up"); m.cursor != 0 {
		t.Errorf("up at the top wrapped to %d", m.cursor)
	}
	if m := press(base, "down", "down"); m.cursor != 1 {
		t.Errorf("down past the end reached %d", m.cursor)
	}
	view := base.View()
	if !strings.Contains(view, "> No") || !strings.Contains(view, "(safe)") || !strings.Contains(view, "Trust this file?") {
		t.Fatalf("view = %q", view)
	}
}

func stubClipboard(t *testing.T, name string, run func(string, string) error) {
	t.Helper()
	oldName, oldRun := clipboardCommandName, runClipboardCommand
	clipboardCommandName = func() string { return name }
	runClipboardCommand = run
	t.Cleanup(func() {
		clipboardCommandName, runClipboardCommand = oldName, oldRun
	})
}

func TestClipboardCopyPrefersLocalClipboardCommand(t *testing.T) {
	home := t.TempDir()
	var buf bytes.Buffer
	var gotName, gotInput string
	stubClipboard(t, "pbcopy", func(name, input string) error {
		gotName, gotInput = name, input
		return nil
	})
	if err := clipboardCopy(&buf, home, "hello"); err != nil {
		t.Fatal(err)
	}
	if gotName != "pbcopy" || gotInput != "hello" {
		t.Fatalf("local command = %q input %q", gotName, gotInput)
	}
	if buf.Len() != 0 {
		t.Fatalf("OSC 52 should not be emitted after local copy succeeds: %q", buf.String())
	}
	b, err := os.ReadFile(filepath.Join(home, lastCopyFile)) //nolint:gosec // test-owned temp dir
	if err != nil || string(b) != "hello" {
		t.Fatalf("backup = %q err %v", b, err)
	}
}

func TestClipboardCopyOSC52AndBackup(t *testing.T) {
	home := t.TempDir()
	var buf bytes.Buffer
	stubClipboard(t, "", func(string, string) error {
		t.Fatal("local clipboard command should not run")
		return nil
	})
	if err := clipboardCopy(&buf, home, "hello"); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.HasPrefix(got, "\x1b]52;c;") || !strings.HasSuffix(got, "\a") {
		t.Fatalf("osc52 frame = %q", got)
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(got, "\x1b]52;c;"), "\a")
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || string(raw) != "hello" {
		t.Fatalf("payload = %q err %v", raw, err)
	}
	b, err := os.ReadFile(filepath.Join(home, lastCopyFile)) //nolint:gosec // test-owned temp dir
	if err != nil || string(b) != "hello" {
		t.Fatalf("backup = %q err %v", b, err)
	}
	if err := clipboardCopy(&buf, home, ""); err == nil {
		t.Fatal("empty copy must fail")
	}
}

func TestClipboardCopyFallsBackToOSC52WhenLocalCommandFails(t *testing.T) {
	home := t.TempDir()
	var buf bytes.Buffer
	stubClipboard(t, "pbcopy", func(string, string) error {
		return errors.New("no clipboard")
	})
	if err := clipboardCopy(&buf, home, "hello"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "\x1b]52;c;") {
		t.Fatalf("missing OSC 52 fallback: %q", buf.String())
	}
}

func TestSaveCopyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out", "reply.md")
	if err := saveCopyFile(path, "hi"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path) //nolint:gosec // test-owned temp dir
	if err != nil || string(b) != "hi" {
		t.Fatalf("wrote %q err %v", b, err)
	}
}

func TestFileCompleter(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("docs/shot.png")
	mk("main.go")
	mk(".git/config")
	mk("node_modules/pkg/index.js")
	c := fileCompleter(root)
	got := c("shot")
	if len(got) != 1 || got[0] != filepath.Join("docs", "shot.png") {
		t.Fatalf("shot: %v", got)
	}
	all := c("")
	if len(all) != 2 { // dot and dependency dirs stay hidden
		t.Fatalf("all: %v", all)
	}
	if got := c("zzz"); len(got) != 0 {
		t.Fatalf("no match: %v", got)
	}
}
