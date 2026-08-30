package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/workspace"
)

func touchFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverRules(t *testing.T) {
	cases := []struct {
		name        string
		rootFiles   []string
		homeFiles   []string
		configured  []string
		wantProject []string
		wantGlobal  int
	}{
		{"agents preferred over claude", []string{"AGENTS.md", "CLAUDE.md"}, nil, nil, []string{"AGENTS.md"}, 0},
		{"claude fallback", []string{"CLAUDE.md"}, nil, nil, []string{"CLAUDE.md"}, 0},
		{"nothing to discover", nil, nil, nil, nil, 0},
		{"configured agents not duplicated", []string{"AGENTS.md"}, nil, []string{"AGENTS.md"}, []string{"AGENTS.md"}, 0},
		{"configured extras kept and agents appended", []string{"AGENTS.md"}, nil, []string{"docs/rules.md"}, []string{"docs/rules.md", "AGENTS.md"}, 0},
		{"global agents found", nil, []string{"AGENTS.md"}, nil, nil, 1},
		{"repo and global together", []string{"AGENTS.md"}, []string{"AGENTS.md"}, nil, []string{"AGENTS.md"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			for _, f := range tc.rootFiles {
				touchFile(t, filepath.Join(root, f))
			}
			for _, f := range tc.homeFiles {
				touchFile(t, filepath.Join(home, f))
			}
			project, global := discoverRules(root, home, tc.configured)
			if len(project) != len(tc.wantProject) {
				t.Fatalf("project = %v, want %v", project, tc.wantProject)
			}
			for i := range project {
				if project[i] != tc.wantProject[i] {
					t.Fatalf("project = %v, want %v", project, tc.wantProject)
				}
			}
			if len(global) != tc.wantGlobal {
				t.Fatalf("global = %v, want %d entries", global, tc.wantGlobal)
			}
			if tc.wantGlobal == 1 && global[0] != filepath.Join(home, "AGENTS.md") {
				t.Fatalf("global[0] = %q, want home AGENTS.md", global[0])
			}
		})
	}
}

func TestDiscoverRulesEmptyHome(t *testing.T) {
	root := t.TempDir()
	touchFile(t, filepath.Join(root, "AGENTS.md"))
	project, global := discoverRules(root, "", nil)
	if len(project) != 1 || project[0] != "AGENTS.md" || global != nil {
		t.Fatalf("project=%v global=%v", project, global)
	}
}

func TestDiscoverRulesDirectoryIgnored(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "AGENTS.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "AGENTS.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	project, global := discoverRules(root, home, nil)
	if len(project) != 0 || global != nil {
		t.Fatalf("directories must be ignored: project=%v global=%v", project, global)
	}
}

func TestLSPManagerFromConfig(t *testing.T) {
	if m := lspManager(nil, "/r"); m != nil {
		t.Fatal("no config must mean no manager")
	}
	cfg := map[string]config.LSPServerConfig{
		"gopls": {Command: []string{"gopls"}, Extensions: []string{".go"}, Enabled: true},
		"off":   {Command: []string{"x"}, Extensions: []string{".x"}},
	}
	if m := lspManager(cfg, "/r"); m == nil {
		t.Fatal("enabled server must build a manager")
	}
	if m := lspManager(map[string]config.LSPServerConfig{"off": cfg["off"]}, "/r"); m != nil {
		t.Fatal("disabled-only config must mean no manager")
	}
}

func TestImageAttachments(t *testing.T) {
	root := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G'}
	if err := os.WriteFile(filepath.Join(root, "shot.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	parts, warns := imageAttachments(root, "look at @shot.png please")
	if len(parts) != 1 || len(warns) != 0 {
		t.Fatalf("explicit: %d parts %v warns", len(parts), warns)
	}
	if parts[0].MediaType != "image/png" || parts[0].Data == "" {
		t.Fatalf("part: %+v", parts[0])
	}
	// Bare existing path also attaches; trailing punctuation is stripped.
	parts, warns = imageAttachments(root, "see shot.png.")
	if len(parts) != 1 || len(warns) != 0 {
		t.Fatalf("bare: %d parts %v warns", len(parts), warns)
	}
	// Explicit miss warns; bare miss stays silent; non-image tokens ignored.
	parts, warns = imageAttachments(root, "@ghost.png missing.png notes.txt @main.go")
	if len(parts) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "ghost.png") {
		t.Fatalf("misses: parts %d warns %v", len(parts), warns)
	}
	// Duplicates collapse; abs path works.
	abs := filepath.Join(root, "shot.png")
	parts, _ = imageAttachments(root, "@"+abs+" "+abs)
	if len(parts) != 1 {
		t.Fatalf("dup/abs: %d parts", len(parts))
	}
}

func TestImageAttachmentCaps(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, maxImageBytes+1)
	if err := os.WriteFile(filepath.Join(root, "big.png"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	_, warns := imageAttachments(root, "@big.png")
	if len(warns) != 1 || !strings.Contains(warns[0], "image cap") {
		t.Fatalf("size cap: %v", warns)
	}
	var sb strings.Builder
	for i := 0; i < maxImages+1; i++ {
		name := filepath.Join(root, "s"+strings.Repeat("x", i)+".png")
		if err := os.WriteFile(name, []byte{1}, 0o600); err != nil {
			t.Fatal(err)
		}
		sb.WriteString("@" + name + " ")
	}
	parts, warns := imageAttachments(root, sb.String())
	if len(parts) != maxImages || len(warns) != 1 || !strings.Contains(warns[0], "image cap") {
		t.Fatalf("count cap: %d parts %v", len(parts), warns)
	}
}

func TestWorktreeOpts(t *testing.T) {
	base := workspace.Options{Root: "/proj", Project: core.NewProjectID()}
	environ := []string{"HOME=" + t.TempDir()}
	same, err := worktreeOpts(base, environ, "")
	if err != nil || same != base {
		t.Fatalf("empty name changed options: %+v, %v", same, err)
	}
	got, err := worktreeOpts(base, environ, "api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != workspace.ModeWorktree || got.Worktree != "api" {
		t.Fatalf("mode/name = %q/%q", got.Mode, got.Worktree)
	}
	if got.WorktreeDir == "" || !strings.Contains(got.WorktreeDir, "worktrees") {
		t.Fatalf("worktree dir %q", got.WorktreeDir)
	}
	// Distinct roots land in distinct parents so names never collide.
	other, err := worktreeOpts(workspace.Options{Root: "/elsewhere"}, environ, "api")
	if err != nil {
		t.Fatal(err)
	}
	if other.WorktreeDir == got.WorktreeDir {
		t.Fatal("different roots share a worktree parent")
	}
}

func TestWorktreeLabel(t *testing.T) {
	if got := worktreeLabel(core.Workspace{Kind: core.WorkspacePrimary, Branch: "main"}); got != "" {
		t.Fatalf("primary labeled %q", got)
	}
	got := worktreeLabel(core.Workspace{Kind: core.WorkspaceWorktree, Branch: "ink/api"})
	if !strings.Contains(got, "ink/api") {
		t.Fatalf("label %q misses the branch", got)
	}
}
