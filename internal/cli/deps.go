package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/lsp"
	"github.com/ataidesorg/ink/internal/runtime"
	"github.com/ataidesorg/ink/internal/workspace"
)

// discoverRules resolves the instruction files a session loads: the configured
// [project] instructions plus an auto-discovered repo rules file (AGENTS.md,
// or CLAUDE.md only when no AGENTS.md exists) and the user's global rules at
// <ink-home>/AGENTS.md. Repo paths stay relative so the runtime confines
// them to the project root; the global path is absolute and CLI-owned.
func discoverRules(root, home string, configured []string) (project, global []string) {
	project = slices.Clone(configured)
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if slices.Contains(project, name) {
			break
		}
		if fi, err := os.Stat(filepath.Join(root, name)); err == nil && !fi.IsDir() {
			project = append(project, name)
			break
		}
	}
	if home != "" {
		if p := filepath.Join(home, "AGENTS.md"); p != "" {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				global = []string{p}
			}
		}
	}
	return project, global
}

// yesApprover pre-approves, for the session, the two risks a coding run needs:
// workspace writes and allow-listed commands (a command reaches approval only
// after the argv allowlist passed it). Destructive, privileged, network and
// secret-bearing requests still go to fallback, which denies when nil.
func yesApprover(fallback runtime.ApprovalFunc) runtime.ApprovalFunc {
	by := core.Principal{Kind: core.PrincipalUser, Name: "--yes"}
	return func(ctx context.Context, a core.Approval) (core.ApprovalResolution, error) {
		switch a.Request.Capability.Risk {
		case core.RiskWriteLocal, core.RiskExecuteLocal:
			return core.ApprovalResolution{Decision: core.ApprovalApproved, By: by, At: time.Now(), Scope: core.ApprovalSession, Note: "pre-approved by --yes"}, nil
		}
		if fallback == nil {
			return core.ApprovalResolution{Decision: core.ApprovalDenied, By: by, At: time.Now(), Scope: core.ApprovalOnce, Note: "--yes never approves " + string(a.Request.Capability.Risk)}, nil
		}
		return fallback(ctx, a)
	}
}

// sandboxSpec turns the sandbox section into a spec rooted at workdir.
func sandboxSpec(c config.SandboxConfig, workdir string) core.SandboxSpec {
	spec := core.NewSandboxSpec(workdir)
	spec.Limits = core.ResourceLimits{
		CPUCores:     c.Limits.CPUCores,
		MemoryMB:     c.Limits.MemoryMB,
		DiskMB:       c.Limits.DiskMB,
		MaxProcesses: c.Limits.MaxProcesses,
		WallClock:    time.Duration(c.Limits.WallClockSecs) * time.Second,
	}
	return spec
}

// withProjectCommands returns a tools section whose command allowlist also
// holds every project.commands.* argv: the runtime runs those commands itself
// during verification, so letting the model call them adds no new capability.
// tools.commands.allowed from the repository stays behind the trust gate.
func withProjectCommands(t config.ToolsConfig, commands map[string]string) (config.ToolsConfig, error) {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	allowed := append([]string(nil), t.Commands.Allowed...)
	for _, name := range names {
		argv, err := splitCommand(commands[name])
		if err != nil {
			return t, fmt.Errorf("project.commands.%s: %w", name, err)
		}
		// ponytail: policy re-splits on whitespace, so a quoted argument with
		// spaces does not survive the round trip; fix by passing argv when needed.
		allowed = append(allowed, strings.Join(argv, " "))
	}
	out := t
	out.Commands.Allowed = allowed
	return out, nil
}

// testCommand is project.commands.test as argv, or nil when unset.
func testCommand(commands map[string]string) ([]string, error) {
	s, ok := commands["test"]
	if !ok || strings.TrimSpace(s) == "" {
		return nil, nil
	}
	return splitCommand(s)
}

// budgetFrom caps the task at budgets.per_task_usd; zero means unset.
func budgetFrom(b config.BudgetsConfig) (core.TaskBudget, error) {
	if b.PerTaskUSD <= 0 {
		return core.TaskBudget{}, nil
	}
	limit, err := core.USDFromFloat(b.PerTaskUSD)
	if err != nil {
		return core.TaskBudget{}, fmt.Errorf("budgets.per_task_usd: %w", err)
	}
	return core.TaskBudget{MaxCost: limit}, nil
}

// lspManager builds the session's language-server manager from config, or
// nil when no server is enabled. Callers wrap tools only on non-nil so a
// typed nil never hides inside the Diagnoser interface.
func lspManager(cfg map[string]config.LSPServerConfig, root string) *lsp.Manager {
	var servers []lsp.Server
	for name, s := range cfg {
		if s.Enabled {
			servers = append(servers, lsp.Server{Name: name, Command: s.Command, Extensions: s.Extensions})
		}
	}
	if len(servers) == 0 {
		return nil
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return lsp.NewManager(root, servers)
}

// worktreeOpts turns a --worktree flag into workspace options: the named
// worktree lives under <ink-home>/worktrees/<hash-of-root>, outside the
// checkout, so it never shows up as dirt in the primary tree. An empty name
// returns o unchanged.
func worktreeOpts(o workspace.Options, environ []string, name string) (workspace.Options, error) {
	if name == "" {
		return o, nil
	}
	home, err := config.Home(envLookup(environ))
	if err != nil {
		return workspace.Options{}, fmt.Errorf("worktree home: %w", err)
	}
	sum := sha256.Sum256([]byte(o.Root))
	o.Mode, o.Worktree = workspace.ModeWorktree, name
	o.WorktreeDir = filepath.Join(home, "worktrees", hex.EncodeToString(sum[:4]))
	return o, nil
}

// worktreeLabel is the header tag shown for a worktree session; empty otherwise.
func worktreeLabel(ws core.Workspace) string {
	if ws.Kind != core.WorkspaceWorktree {
		return ""
	}
	return "\u2387 " + ws.Branch
}

func vcsBranch(v *core.VCSInfo) string {
	if v == nil {
		return ""
	}
	return v.Branch
}

func vcsDirty(v *core.VCSInfo) bool {
	return v != nil && v.Dirty
}

const (
	maxImageBytes = 5 << 20
	maxImages     = 8
)

var imageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp",
}

// imageAttachments finds image paths mentioned in a prompt — "@shot.png" or
// a bare existing path (a pasted screenshot path) — and loads them as
// attachments. The prompt text is user-authored, so referenced files are
// user trust; @-explicit misses warn, bare misses stay silent because prose
// mentions files that never existed.
func imageAttachments(root, prompt string) ([]core.ImagePart, []string) {
	var parts []core.ImagePart
	var warns []string
	seen := map[string]bool{}
	for _, tok := range strings.Fields(prompt) {
		tok = strings.TrimRight(tok, ".,;:!?")
		tok = strings.Trim(tok, `"'`)
		tok = strings.TrimRight(tok, ".,;:!?")
		explicit := strings.HasPrefix(tok, "@")
		name := strings.TrimPrefix(tok, "@")
		mt, ok := imageTypes[strings.ToLower(filepath.Ext(name))]
		if !ok || name == "" || seen[name] {
			continue
		}
		seen[name] = true
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, name)
		}
		st, err := os.Stat(path) //nolint:gosec // attachment path comes from the user's own prompt
		switch {
		case err != nil || !st.Mode().IsRegular():
			if explicit {
				warns = append(warns, fmt.Sprintf("attachment %s: not a readable file", name))
			}
			continue
		case st.Size() > maxImageBytes:
			warns = append(warns, fmt.Sprintf("attachment %s: over the %dMB image cap, skipped", name, maxImageBytes>>20))
			continue
		case len(parts) >= maxImages:
			warns = append(warns, fmt.Sprintf("attachment %s: over the %d-image cap, skipped", name, maxImages))
			continue
		}
		b, err := os.ReadFile(path) //nolint:gosec // attachment path comes from the user's own prompt
		if err != nil {
			if explicit {
				warns = append(warns, fmt.Sprintf("attachment %s: %v", name, err))
			}
			continue
		}
		parts = append(parts, core.ImagePart{MediaType: mt, Data: base64.StdEncoding.EncodeToString(b)})
	}
	return parts, warns
}
