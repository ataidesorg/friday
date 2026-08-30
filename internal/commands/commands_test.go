package commands

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
)

func TestParse(t *testing.T) {
	c, err := Parse("deploy", []byte("---\ndescription = \"ship it\"\nmodel = \"fast\"\n---\nDeploy $ARGUMENTS now.\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Description != "ship it" || c.Model != "fast" || c.Body != "Deploy $ARGUMENTS now." {
		t.Fatalf("parsed %+v", c)
	}
	if got := Expand(c, "prod"); got != "Deploy prod now." {
		t.Fatalf("expand %q", got)
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"Bad Name", "x\n"},
		{"help", "x\n"},         // shadows a built-in
		{"empty", "---\n---\n"}, // no prompt body
		{"unknown", "---\nnope = 1\n---\nx\n"},
		{"badtoml", "---\n= broken\n---\nx\n"},
	}
	for _, tc := range cases {
		if _, err := Parse(tc.name, []byte(tc.body), map[string]bool{"help": true}); err == nil {
			t.Fatalf("%s: no error", tc.name)
		} else if !errors.Is(err, core.ErrInvalidInput) && !errors.Is(err, core.ErrConflict) {
			t.Fatalf("%s: err %v", tc.name, err)
		}
	}
}

func TestExpandAppends(t *testing.T) {
	c := Command{Body: "Review this."}
	if got := Expand(c, "src/main.go"); got != "Review this.\n\nsrc/main.go" {
		t.Fatalf("expand %q", got)
	}
	if got := Expand(c, ""); got != "Review this." {
		t.Fatalf("expand %q", got)
	}
}

func TestLoadProjectWins(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, "commands"), "greet.md", "user greeting\n")
	write(filepath.Join(home, "commands"), "solo.md", "only in home\n")
	write(filepath.Join(root, ".ink", "commands"), "greet.md", "project greeting\n")
	write(filepath.Join(root, ".ink", "commands"), "broken.md", "---\nnever closed\n")
	write(filepath.Join(root, ".ink", "commands"), "notes.txt", "not a command\n")

	var warn strings.Builder
	got := Load(root, home, &warn, nil)
	if len(got) != 2 || got[0].Name != "greet" || got[1].Name != "solo" {
		t.Fatalf("loaded %+v", got)
	}
	if got[0].Body != "project greeting" {
		t.Fatalf("project should win: %q", got[0].Body)
	}
	if !strings.Contains(warn.String(), "broken") {
		t.Fatalf("bad file not reported: %q", warn.String())
	}
}

func TestLoadNoDirs(t *testing.T) {
	if got := Load(t.TempDir(), "", nil, nil); len(got) != 0 {
		t.Fatalf("loaded %v", got)
	}
}

func TestLoadSkipsReserved(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".ink", "commands")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "help.md"), []byte("should not load\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "greet.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warn strings.Builder
	got := Load(root, "", &warn, map[string]bool{"help": true})
	if len(got) != 1 || got[0].Name != "greet" {
		t.Fatalf("loaded %+v", got)
	}
	if !strings.Contains(warn.String(), "help") {
		t.Fatalf("reserved skip not reported: %q", warn.String())
	}
}
