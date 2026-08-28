package cli

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ataidesorg/friday/internal/config"
	"github.com/ataidesorg/friday/internal/redact"
	"github.com/ataidesorg/friday/internal/tui"
)

// multiFlag collects repeated --set key=value flags.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

type globalFlags struct {
	project, profile, configDir string
	set                         multiFlag
}

// bind registers the flags every configuration-driven command shares.
func (g *globalFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&g.project, "project", "", "project root")
	fs.StringVar(&g.profile, "profile", "", "profile name")
	fs.StringVar(&g.configDir, "config-dir", "", "user config directory")
	fs.Var(&g.set, "set", "key=value override (repeatable)")
}

// parseInterleaved parses flags wherever they appear and returns the positionals.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func parseGlobal(name string, args []string, stderr io.Writer) (globalFlags, []string, error) {
	var g globalFlags
	fs := flag.NewFlagSet("config "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	g.bind(fs)
	positional, err := parseInterleaved(fs, args)
	return g, positional, err
}

func (g globalFlags) options(environ []string, getwd func() (string, error), stderr io.Writer) (config.Options, error) {
	opts := config.Options{ConfigDir: g.configDir, Profile: g.profile, ProjectRoot: g.project, Environ: environ, Overrides: map[string]string{}}
	if opts.ConfigDir == "" {
		dir, err := config.Dir(envLookup(environ))
		if err != nil {
			fmt.Fprintf(stderr, "warning: user config skipped: %v\n", err)
		}
		opts.ConfigDir = dir
	}
	if opts.ProjectRoot == "" {
		wd, err := getwd()
		if err != nil {
			return opts, fmt.Errorf("resolve project root: %w", err)
		}
		opts.ProjectRoot = wd
	}
	if store, err := trustStore(environ); err != nil {
		fmt.Fprintf(stderr, "warning: repository trust unavailable: %v\n", err)
	} else {
		opts.Trust = store
	}
	for _, kv := range g.set {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return opts, fmt.Errorf("--set expects key=value, got %q", kv)
		}
		opts.Overrides[key] = value
	}
	return opts, nil
}

func envLookup(environ []string) func(string) string {
	lookup := envLookupOK(environ)
	return func(name string) string {
		v, _ := lookup(name)
		return v
	}
}

func configCmd(args []string, stdout, stderr io.Writer, environ []string, getwd func() (string, error)) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: friday config <show|validate|explain KEY> [flags]")
		return exitUsage
	}
	sub := args[0]
	if sub != "show" && sub != "validate" && sub != "explain" {
		fmt.Fprintf(stderr, "friday config: unknown subcommand %q\n", sub)
		return exitUsage
	}
	g, positional, err := parseGlobal(sub, args[1:], stderr)
	if err != nil {
		return exitUsage
	}
	if (sub == "explain" && len(positional) != 1) || (sub != "explain" && len(positional) != 0) {
		fmt.Fprintln(stderr, "usage: friday config <show|validate|explain KEY> [flags]")
		return exitUsage
	}
	opts, err := g.options(environ, getwd, stderr)
	if err != nil {
		return fail(stderr, "config", exitUsage, err)
	}
	resolved, err := config.Load(opts)
	if err != nil {
		return fail(stderr, "config", exitError, err)
	}
	verr := config.Validate(resolved)
	out := redact.New()
	warnDropped(stderr, resolved)
	if sub == "validate" {
		if verr != nil {
			fmt.Fprintln(stderr, out.Redact(verr.Error()))
			return exitError
		}
		fmt.Fprintln(stdout, "ok")
		return exitOK
	}
	if sub == "show" {
		// Never dump a configuration that failed validation: it may hold a secret literal.
		if verr != nil {
			fmt.Fprintf(stderr, "friday config: invalid configuration\n%s\n", out.Redact(verr.Error()))
			return exitError
		}
		text, err := resolved.TOML()
		if err != nil {
			return fail(stderr, "config", exitError, err)
		}
		fmt.Fprint(stdout, out.Redact(text))
		return exitOK
	}
	ex, ok := resolved.Explain(positional[0])
	if !ok {
		fmt.Fprintf(stderr, "friday config explain: unknown key %q\n", positional[0])
		return exitError
	}
	fmt.Fprintln(stdout, out.Redact(ex.String()))
	if verr != nil {
		fmt.Fprintf(stderr, "warning: configuration is invalid; run `friday config validate`\n")
	}
	return exitOK
}

// settings are Friday's user-state knobs the TUI persists live — the choices
// a picker remembers, distinct from the declarative config files. Stored as
// JSON in the Friday home.
type settings struct {
	Theme   string `json:"theme,omitempty"`
	VimMode bool   `json:"vim_mode,omitempty"`
}

func settingsPath(home string) string { return filepath.Join(home, "settings.json") }

// loadSettings reads the saved state; a missing or corrupt file is a fresh
// start, never a launch failure.
func loadSettings(home string) settings {
	var s settings
	b, err := os.ReadFile(settingsPath(home))
	if err != nil {
		return settings{}
	}
	if json.Unmarshal(b, &s) != nil {
		return settings{}
	}
	return s
}

func saveSettings(home string, s settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("create friday home: %w", err)
	}
	if err := os.WriteFile(settingsPath(home), append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}

// loadThemes parses every custom theme under <home>/themes. A bad file is
// reported on warn and skipped; themes never block a launch. Theme files are
// the user's own local palettes — no secrets flow through here.
func loadThemes(home string, warn io.Writer) []tui.Theme {
	dir := filepath.Join(home, "themes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []tui.Theme
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // the user's own theme files under the Friday home
		if err != nil {
			fmt.Fprintf(warn, "friday: theme %s skipped: %v\n", e.Name(), err)
			continue
		}
		th, err := tui.ParseTheme(strings.TrimSuffix(e.Name(), ".toml"), b)
		if err != nil {
			fmt.Fprintf(warn, "friday: %v\n", err)
			continue
		}
		out = append(out, th)
	}
	return out
}

// loadUserEnv pulls KEY=value pairs from <friday-home>/env into the process
// environment when the key is not already set. Existing exports win. The
// file is never printed. main() calls this so `friday` works without a
// manual `source ~/.friday/env`; tests call run() and skip it.
func loadUserEnv() {
	home, err := config.FridayHome(os.Getenv)
	if err != nil {
		return
	}
	_ = applyEnvFile(filepath.Join(home, "env"), os.Setenv, os.Getenv)
}

func applyEnvFile(path string, setenv func(string, string) error, getenv func(string) string) error {
	f, err := os.Open(path) //nolint:gosec // operator-owned ~/.friday/env
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close() //nolint:errcheck // best-effort close of a read-only env file
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		if getenv(k) != "" {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 {
			if q := v[0]; (q == '"' || q == '\'') && v[len(v)-1] == q {
				v = v[1 : len(v)-1]
			}
		}
		if err := setenv(k, v); err != nil {
			return err
		}
	}
	return sc.Err()
}
