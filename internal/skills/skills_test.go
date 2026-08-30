package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
)

func write(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadProjectWins(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, "skills", "deploy"), "---\ndescription = \"how to deploy\"\n---\nuser steps\n")
	write(t, filepath.Join(root, "skills", "deploy"), "---\ndescription = \"project deploy\"\n---\nproject steps\n")
	write(t, filepath.Join(root, "skills", "renamed"), "---\nname = \"review\"\ndescription = \"code review\"\n---\nreview steps\n")
	write(t, filepath.Join(root, "skills", "nodesc"), "---\nname = \"x\"\n---\nbody\n")
	if err := os.MkdirAll(filepath.Join(root, "skills", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}

	var warn strings.Builder
	got := Load(root, home, &warn)
	if len(got) != 2 || got[0].Name != "deploy" || got[1].Name != "review" {
		t.Fatalf("loaded %+v", got)
	}
	if got[0].Content != "project steps" {
		t.Fatalf("project should win: %q", got[0].Content)
	}
	if !strings.HasSuffix(got[0].Path, "SKILL.md") {
		t.Fatalf("path not recorded: %q", got[0].Path)
	}
	if !strings.Contains(warn.String(), "nodesc") {
		t.Fatalf("missing description not reported: %q", warn.String())
	}
	if strings.Contains(warn.String(), "empty") {
		t.Fatalf("bare directory should be silent: %q", warn.String())
	}
}

func TestTool(t *testing.T) {
	tool := Tool([]Skill{{Name: "deploy", Description: "d", Content: "steps here"}})
	if tool.Spec().Name != "skill" || tool.Spec().Risk != core.RiskReadOnly {
		t.Fatalf("spec %+v", tool.Spec())
	}
	out, err := tool.Invoke(context.Background(), core.ToolInput{Arguments: []byte(`{"name":"deploy"}`)}, core.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Content, "steps here") {
		t.Fatalf("content %q", out.Content)
	}
	if _, err := tool.Invoke(context.Background(), core.ToolInput{Arguments: []byte(`{"name":"nope"}`)}, core.ToolContext{}); err == nil || !strings.Contains(err.Error(), "deploy") {
		t.Fatalf("unknown skill should list available, got %v", err)
	}
	if _, err := tool.Invoke(context.Background(), core.ToolInput{Arguments: []byte(`{"bad":1}`)}, core.ToolContext{}); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestLoadSizeCap(t *testing.T) {
	root := t.TempDir()
	big := "---\ndescription = \"d\"\n---\n" + strings.Repeat("x", maxSkillSize)
	write(t, filepath.Join(root, "skills", "huge"), big)
	var warn strings.Builder
	if got := Load(root, "", &warn); len(got) != 0 {
		t.Fatalf("loaded %+v", got)
	}
	if !strings.Contains(warn.String(), "huge") {
		t.Fatalf("size cap not reported: %q", warn.String())
	}
}
