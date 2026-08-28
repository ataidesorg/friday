package tui

import "testing"

func TestAssistantReplyCopyHelpers(t *testing.T) {
	lines := []string{
		tagUser + " first",
		tagReply + " a1",
		tagReply + " a1b",
		tagUser + " second",
		tagReply + " a2",
		tagTool + " read_file ok",
	}
	if got := lastAssistantReply(lines); got != "a2" {
		t.Fatalf("last = %q", got)
	}
	if got := nthAssistantReply(lines, 2); got != "a1\na1b" {
		t.Fatalf("2nd last = %q", got)
	}
	if got := nthAssistantReply(lines, 9); got != "" {
		t.Fatalf("missing nth = %q", got)
	}
	if got := blockPlain(lines, 1); got != "a1\na1b" {
		t.Fatalf("block = %q", got)
	}
	if got := rewindLines(lines, 1); len(got) != 3 || got[0] != tagUser+" first" {
		t.Fatalf("rewind 1 = %#v", got)
	}
	items := userPromptItems(lines)
	if len(items) != 2 || items[0].id != "1" || items[1].title != "second" {
		t.Fatalf("prompts = %+v", items)
	}
}

func TestCleanCopiedPaneRowsStripsFrameRails(t *testing.T) {
	rows := []string{
		"  ╭ Friday ─────────────────────────╮",
		"  │ Hi! I'm Friday.                  │",
		"  │                                   │",
		"  │ • Build and test                  │",
		"  ╰───────────────────────────────────╯",
	}
	got := cleanCopiedPaneRows(rows)
	want := "Hi! I'm Friday.\n\n• Build and test"
	if got != want {
		t.Fatalf("cleaned copy = %q, want %q", got, want)
	}
}

func TestCleanCopiedPaneRowsStripsANSIFrameRails(t *testing.T) {
	rows := []string{
		"\x1b[90m│\x1b[0m copied body \x1b[90m│\x1b[0m",
	}
	if got := cleanCopiedPaneRows(rows); got != "copied body" {
		t.Fatalf("cleaned ANSI copy = %q", got)
	}
}
