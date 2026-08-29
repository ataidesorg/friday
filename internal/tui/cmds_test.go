package tui

import (
	"strings"
	"testing"
)

func TestBuiltinCommandsAreComplete(t *testing.T) {
	loadCmds()
	ids, slashes := map[string]bool{}, map[string]bool{}
	for _, c := range cmdIndex.all {
		if c.ID == "" || c.Title == "" || c.Group == "" || c.Run == nil {
			t.Fatalf("incomplete command: %+v", c)
		}
		if ids[c.ID] {
			t.Fatalf("duplicate id %q", c.ID)
		}
		ids[c.ID] = true
		if c.Slash == "" {
			continue
		}
		if slashes[c.Slash] {
			t.Fatalf("duplicate slash /%s", c.Slash)
		}
		slashes[c.Slash] = true
	}
	if !ids["cycle-mode"] {
		t.Fatal("palette-only cycle-mode missing")
	}
	if slashes["cycle-mode"] {
		t.Fatal("cycle-mode must not be a slash command")
	}
	if _, ok := lookupSlash("tools"); !ok {
		t.Fatal("/tools missing")
	}
	if _, ok := lookupID("tools-display"); !ok {
		t.Fatal("palette id tools-display missing")
	}
}

func TestSlashAndPaletteShareTheTable(t *testing.T) {
	loadCmds()
	if len(chatActions()) != len(cmdIndex.all) {
		t.Fatalf("palette %d rows, table %d", len(chatActions()), len(cmdIndex.all))
	}
	got := map[string]bool{}
	for _, e := range builtinSlash() {
		if got[e.name] {
			t.Fatalf("duplicate slash name %q", e.name)
		}
		got[e.name] = true
	}
	names := SlashNames()
	if len(names) != len(got) {
		t.Fatalf("SlashNames %d, typeahead %d", len(names), len(got))
	}
	for _, n := range names {
		if !got[n] {
			t.Fatalf("SlashNames has %q but typeahead does not", n)
		}
	}
	for _, want := range []string{
		"help", "fork", "export", "timestamps", "advisories", "always-approve",
		"goal", "queue", "commands", "edit-prompt", "cost", "doctor", "clear",
		"plan", "rewind", "verbose", "connect", "quit",
	} {
		if !got[want] {
			t.Fatalf("slash typeahead missing /%s", want)
		}
	}
	ids := map[string]bool{}
	for _, it := range chatActions() {
		ids[it.id] = true
	}
	for _, want := range []string{"fork", "export", "timestamps", "advisories", "always-approve", "cost", "doctor", "clear", "plan", "rewind", "commands", "cycle-mode"} {
		if !ids[want] {
			t.Fatalf("Ctrl+P missing %q", want)
		}
	}
}

func TestEverySlashDispatches(t *testing.T) {
	loadCmds()
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	for _, c := range cmdIndex.all {
		if c.Slash == "" {
			continue
		}
		next, cmd := m.command("/" + c.Slash)
		cm, ok := next.(ChatModel)
		if !ok {
			t.Fatalf("/%s returned %T", c.Slash, next)
		}
		if strings.Contains(strings.Join(cm.Lines, "\n"), "unknown command") {
			t.Fatalf("/%s is in the table but dispatch does not know it", c.Slash)
		}
		if c.Slash == "quit" || c.Slash == "exit" {
			if cmd == nil {
				t.Fatalf("/%s did not quit", c.Slash)
			}
		}
	}
}

func TestTogglesPersistPrefs(t *testing.T) {
	var got Prefs
	m := NewChat(Options{
		Width: 80, NoColor: true,
		SetPrefs: func(p Prefs) error { got = p; return nil },
	}, nil)
	next, _ := m.command("/verbose")
	m = next.(ChatModel)
	if !got.Verbose || !got.ShowTools || !got.ShowThinking {
		t.Fatalf("verbose persist = %+v", got)
	}
	next, _ = m.command("/tools")
	m = next.(ChatModel)
	if got.ShowTools {
		t.Fatalf("tools persist still on: %+v", got)
	}
	next, _ = m.overlayCommit(overlayPalette, "timestamps")
	if !next.(ChatModel).showTimes || !got.Timestamps {
		t.Fatalf("palette timestamps persist = %+v showTimes=%v", got, next.(ChatModel).showTimes)
	}
}

func TestPaletteCommitUsesTheTable(t *testing.T) {
	m := NewChat(Options{Width: 80, NoColor: true}, nil)
	next, _ := m.overlayCommit(overlayPalette, "cycle-mode")
	cm := next.(ChatModel)
	if cm.permLabel() == "normal" {
		t.Fatal("cycle-mode from the palette did not advance")
	}
	next, _ = m.overlayCommit(overlayPalette, "tools-display")
	cm = next.(ChatModel)
	if cm.showTools {
		t.Fatal("/tools from the palette should hide tool activity")
	}
	next, _ = m.overlayCommit(overlayPalette, "not-a-command")
	if next.(ChatModel).Lines != nil && strings.Contains(strings.Join(next.(ChatModel).Lines, "\n"), "unknown command") {
		t.Fatal("unknown palette id must not warn as a slash command")
	}
}
