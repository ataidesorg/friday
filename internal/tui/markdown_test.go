package tui

import (
	"strings"
	"testing"
)

func TestPaintMarkdownStripsMarkers(t *testing.T) {
	cs := newChatStyles(false)
	out := cs.paintMarkdownLine("**bold** and `code` and *em*", 80, false)
	if strings.Contains(out, "**") || strings.Contains(out, "`") {
		t.Fatalf("markers survived: %q", out)
	}
	for _, want := range []string{"bold", "code", "em"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	list := cs.paintMarkdownLine("- ship it", 80, false)
	if !strings.Contains(list, "ship it") || strings.Contains(list, "- ship") {
		t.Fatalf("list = %q", list)
	}
	quote := cs.paintMarkdownLine("> note", 80, false)
	if !strings.Contains(quote, "│") || !strings.Contains(quote, "note") {
		t.Fatalf("quote = %q", quote)
	}
	link := cs.paintInline("[docs](https://example.com)")
	if link != "docs" {
		t.Fatalf("link = %q", link)
	}
}

func TestPaintMarkdownInConversation(t *testing.T) {
	cs := newChatStyles(false)
	out := cs.conversation([]string{
		"[ink] ## Title",
		"[ink] Use **todo_write** and `read_file`.",
		"[ink] - first",
		"[ink] - second",
		"[ink] > quoted",
	}, 60, -1)
	for _, want := range []string{"Title", "todo_write", "read_file", "first", "second", "quoted"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "## ") || strings.Contains(out, "**") {
		t.Fatalf("raw markup leaked:\n%s", out)
	}
}
