package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/redact"
)

// proxyVars are dropped from the requested environment; egress is disabled
// by policy, so a proxy would only be a way around that.
var proxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "FTP_PROXY"}

// systemPath is the fixed search path; the host PATH is never consulted
// inside the sandbox.
var systemPath = []string{"/usr/bin", "/bin", "/usr/local/bin"}

// scrubEnv validates the caller's env: keys must be well formed, proxy
// variables are removed, and a secret-shaped key or value is refused.
func scrubEnv(in map[string]string, r *redact.Redactor) (map[string]string, error) {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k == "" || strings.Contains(k, "=") {
			return nil, fmt.Errorf("%w: malformed env key %q", core.ErrInvalidInput, k)
		}
		if slices.Contains(proxyVars, strings.ToUpper(k)) {
			continue
		}
		if r.ContainsSecret(v) || r.ContainsSecret(k+"="+v) {
			return nil, fmt.Errorf("%w: env %s looks like a secret", core.ErrSecretContent, k)
		}
		out[k] = v
	}
	return out, nil
}

// buildEnv merges the scrubbed env under the fixed entries. The Go build
// and module caches are shared with the host read-write: they hold public
// sources and compiled objects, never secrets, and a cold cache per run
// would cost seconds. GOPROXY=off and GOTOOLCHAIN=local keep the toolchain
// from reaching the network under a policy that forbids it.
func buildEnv(scrubbed map[string]string, home string, path []string) []string {
	merged := make(map[string]string, len(scrubbed)+12)
	for k, v := range scrubbed {
		merged[k] = v
	}
	fixed := map[string]string{
		"PATH":        strings.Join(path, string(os.PathListSeparator)),
		"HOME":        home,
		"TMPDIR":      filepath.Join(home, "tmp"),
		"GOCACHE":     hostGoCache(),
		"GOMODCACHE":  hostGoModCache(),
		"GOFLAGS":     "-mod=mod",
		"GOPROXY":     "off",
		"GOTOOLCHAIN": "local",
		"NO_PROXY":    "*",
		"no_proxy":    "*",
	}
	for k, v := range fixed {
		merged[k] = v
	}
	keys := slices.Sorted(maps(merged))
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+merged[k])
	}
	return env
}

func maps(m map[string]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// pathDirs is the system path plus the directory of the host's go binary,
// resolved through symlinks so a package manager's whole bin directory is
// not exposed. Computed once per provider.
func pathDirs() []string {
	dirs := slices.Clone(systemPath)
	if root := os.Getenv("GOROOT"); root != "" {
		dirs = append(dirs, filepath.Join(root, "bin"))
	}
	if p, err := exec.LookPath("go"); err == nil {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			p = resolved
		}
		if d := filepath.Dir(p); !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func hostGoCache() string {
	if v := os.Getenv("GOCACHE"); v != "" {
		return v
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "go-build")
	}
	return ""
}

func hostGoModCache() string {
	if v := os.Getenv("GOMODCACHE"); v != "" {
		return v
	}
	if gp := os.Getenv("GOPATH"); gp != "" {
		return filepath.Join(filepath.SplitList(gp)[0], "pkg", "mod")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "go", "pkg", "mod")
	}
	return ""
}
