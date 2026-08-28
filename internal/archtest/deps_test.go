// Package archtest turns the dependency direction into a test:
// redact ← core ← providers ← {config, fsutil} ← {models, tools, policy, sandbox, observability, workspace} ← runtime ← {tui, evals} ← cli ← cmd/friday.
// internal/providers is the embedded provider registry: pure vocabulary data
// (ids, wires, auth kinds), below config so validation can name unknown kinds.
// New packages must be registered here, and only the sandbox providers and
// the workspace package may spawn processes from product code.
package archtest

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/ataidesorg/friday"

// allowed maps each package to the internal packages it may import.
var allowed = map[string][]string{
	module + "/internal/redact":            {},
	module + "/internal/core":              {"internal/redact"},
	module + "/internal/config":            {"internal/core", "internal/redact", "internal/providers"},
	module + "/internal/session":           {"internal/core", "internal/redact"},
	module + "/internal/fmatter":           {"internal/core"},
	module + "/internal/commands":          {"internal/core", "internal/fmatter"},
	module + "/internal/skills":            {"internal/core", "internal/fmatter"},
	module + "/internal/providers":         {"internal/core"},
	module + "/internal/auth":              {"internal/config", "internal/core", "internal/providers", "internal/redact"},
	module + "/internal/buildinfo":         {},
	module + "/internal/archtest":          {},
	module + "/internal/models":            {"internal/core", "internal/auth", "internal/providers"},
	module + "/internal/models/mock":       {"internal/core"},
	module + "/internal/models/wire":       {"internal/core", "internal/auth"},
	module + "/internal/models/catalog":    {},
	module + "/internal/routing":           {"internal/core", "internal/config"},
	module + "/internal/fsutil":            {"internal/core"},
	module + "/internal/tools":             {"internal/core", "internal/redact", "internal/fsutil"},
	module + "/internal/mcp":               {"internal/core"},
	module + "/internal/lsp":               {},
	module + "/internal/policy":            {"internal/core", "internal/config"},
	module + "/internal/sandbox":           {"internal/core", "internal/redact"},
	module + "/internal/sandbox/process":   {"internal/core", "internal/redact", "internal/sandbox", "internal/fsutil"},
	module + "/internal/sandbox/container": {"internal/core", "internal/redact", "internal/sandbox", "internal/fsutil"},
	module + "/internal/observability":     {"internal/core", "internal/redact"},
	module + "/internal/workspace":         {"internal/core", "internal/fsutil"},
	module + "/internal/runtime":           {"internal/core", "internal/fsutil", "internal/config", "internal/tools", "internal/policy", "internal/models/mock"},
	module + "/internal/tui":               {"internal/core", "internal/runtime"},
	module + "/internal/evals":             {"internal/core", "internal/fsutil", "internal/redact", "internal/tools", "internal/runtime", "internal/workspace", "internal/observability", "internal/config", "internal/policy", "internal/sandbox", "internal/sandbox/process", "internal/models/mock"},
	module + "/internal/cli":               {"internal/core", "internal/config", "internal/redact", "internal/buildinfo", "internal/observability", "internal/runtime", "internal/tui", "internal/policy", "internal/tools", "internal/sandbox", "internal/sandbox/process", "internal/sandbox/container", "internal/workspace", "internal/models/mock", "internal/evals", "internal/providers", "internal/auth", "internal/models", "internal/models/wire", "internal/models/catalog", "internal/routing", "internal/session", "internal/commands", "internal/skills", "internal/mcp", "internal/lsp"},
	// cmd/friday is the process entrypoint and nothing else: two lines
	// over cli.Main, so the whole CLI stays testable as a library.
	module + "/cmd/friday": {"internal/cli"},
}

// execAllowed are the only packages (by prefix) whose product code may
// import os/exec: the sandbox providers, internal/workspace,
// internal/auth (OS keyring reads and user-configured credential commands
// are process spawns by nature; both are user-layer-configured and
// attributable), internal/mcp, internal/lsp, internal/tui (user-initiated
// $VISUAL/$EDITOR for /edit-prompt; argv is the editor plus a temp file
// Friday owns), and internal/cli for the fixed local clipboard helper. internal/workspace runs git argv-only with hooks disabled
// (no shell, fixed subcommands). Test files are exempt: they spawn git and
// `go list` to build fixtures.
var execAllowed = []string{"internal/sandbox/", "internal/workspace", "internal/auth", "internal/mcp", "internal/lsp", "internal/tui", "internal/cli"}

// allowedModules are the only direct third-party modules; adding one is a
// deliberate decision. Transitive modules are not listed: they are
// whatever the direct ones need, and govulncheck plus dependabot watch them.
var allowedModules = []string{
	"github.com/BurntSushi/toml",
	"github.com/google/uuid",
	"github.com/charmbracelet/bubbletea",
	"github.com/charmbracelet/lipgloss",
	"github.com/charmbracelet/bubbles",
}

func goList(t *testing.T, args ...string) []string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list"}, args...)...) //nolint:gosec // args are test constants
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		cmd = exec.Command("go", append([]string{"list"}, args...)...) //nolint:gosec // args are test constants
		cmd.Dir = filepath.Join("..", "..")
		out, _ = cmd.CombinedOutput()
		t.Fatalf("go list %v: %v\n%s", args, err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func TestImportDirection(t *testing.T) {
	lines := goList(t, "-f", `{{.ImportPath}} {{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}`, "./...")
	seen := map[string]bool{}
	for _, line := range lines {
		fields := strings.Fields(line)
		pkg := fields[0]
		seen[pkg] = true
		permitted, registered := allowed[pkg]
		if !registered {
			t.Errorf("package %s is not registered in archtest.allowed", pkg)
			continue
		}
		for _, imp := range fields[1:] {
			if !strings.HasPrefix(imp, module+"/") || imp == pkg {
				continue
			}
			if !slices.Contains(permitted, strings.TrimPrefix(imp, module+"/")) {
				t.Errorf("%s imports %s: not permitted (allowed: %v)", pkg, imp, permitted)
			}
		}
	}
	for pkg := range allowed {
		if !seen[pkg] {
			t.Errorf("registered package %s no longer exists", pkg)
		}
	}
}

func TestExecConfinedToSandbox(t *testing.T) {
	lines := goList(t, "-f", `{{.ImportPath}} {{join .Imports " "}}`, "./...")
	for _, line := range lines {
		fields := strings.Fields(line)
		if !slices.Contains(fields[1:], "os/exec") {
			continue
		}
		rel := strings.TrimPrefix(fields[0], module+"/")
		if !slices.ContainsFunc(execAllowed, func(p string) bool { return strings.HasPrefix(rel, p) }) {
			t.Errorf("%s imports os/exec: only %v may spawn processes", fields[0], execAllowed)
		}
	}
}

func TestThirdPartyModules(t *testing.T) {
	for _, m := range goList(t, "-m", "-f", "{{.Path}} {{.Indirect}}", "all") {
		fields := strings.Fields(m)
		path, indirect := fields[0], len(fields) > 1 && fields[1] == "true"
		if indirect || path == module || strings.HasPrefix(path, "golang.org/") || slices.Contains(allowedModules, path) {
			continue
		}
		t.Errorf("unexpected direct module %s: add it to allowedModules and say why in the commit message", path)
	}
}
