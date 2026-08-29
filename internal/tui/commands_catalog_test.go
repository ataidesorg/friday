package tui

import "testing"

func TestCommandCatalogIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range commandCatalog() {
		if c.id == "" || c.title == "" || c.group == "" {
			t.Fatalf("incomplete catalog row: %+v", c)
		}
		if seen[c.id] {
			t.Fatalf("duplicate catalog id %q", c.id)
		}
		seen[c.id] = true
	}
}

func TestSlashAndPaletteShareTheCatalog(t *testing.T) {
	if len(chatActions()) != len(commandCatalog()) {
		t.Fatalf("palette %d rows, catalog %d", len(chatActions()), len(commandCatalog()))
	}
	got := map[string]bool{}
	for _, e := range builtinSlash() {
		if got[e.name] {
			t.Fatalf("duplicate slash name %q", e.name)
		}
		got[e.name] = true
	}
	for _, want := range []string{
		"help", "fork", "export", "timestamps", "advisories", "always-approve",
		"goal", "queue", "commands", "edit-prompt", "cost", "doctor", "clear",
		"plan", "rewind", "verbose", "connect",
	} {
		if !got[want] {
			t.Fatalf("slash typeahead missing /%s", want)
		}
	}
	ids := map[string]bool{}
	for _, it := range chatActions() {
		ids[it.id] = true
	}
	for _, want := range []string{"fork", "export", "timestamps", "advisories", "always-approve", "cost", "doctor", "clear", "plan", "rewind", "commands"} {
		if !ids[want] {
			t.Fatalf("Ctrl+P missing %q", want)
		}
	}
}
