package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ataidesorg/friday/internal/config"
	"github.com/ataidesorg/friday/internal/observability"
)

// ignoredPaths are the repository-local files Friday writes that must never be committed.
var ignoredPaths = []string{config.ProjectLocalConfigRelPath, observability.LocalDir + "/"}

// initCmd creates .friday/config.toml when absent and adds Friday's local
// paths to .gitignore when absent. It never overwrites anything.
func initCmd(args []string, stdout, stderr io.Writer, getwd func() (string, error)) int {
	var project string
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&project, "project", "", "project root (default: current directory)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: friday init [--project DIR]")
		return exitUsage
	}
	root := project
	if root == "" {
		var err error
		if root, err = getwd(); err != nil {
			return fail(stderr, "init", exitError, err)
		}
	}
	if err := initProject(root, stdout); err != nil {
		return fail(stderr, "init", exitError, err)
	}
	return exitOK
}

func initProject(root string, stdout io.Writer) error {
	cfgPath := filepath.Join(root, filepath.FromSlash(config.ProjectConfigRelPath))
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o750); err != nil {
		return err
	}
	name := filepath.Base(root)
	if abs, err := filepath.Abs(root); err == nil {
		name = filepath.Base(abs)
	}
	content := "[project]\nname = " + strconv.Quote(name) + "\n"
	if err := writeIfAbsent(cfgPath, content); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s ready\n", cfgPath)
	return ensureIgnored(filepath.Join(root, ".gitignore"), ignoredPaths)
}

// writeIfAbsent creates path with content; an existing file is left alone.
func writeIfAbsent(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path is the project's own .friday/config.toml
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ensureIgnored appends each line missing from the .gitignore at path.
func ensureIgnored(path string, lines []string) error {
	existing, err := os.ReadFile(path) //nolint:gosec // the project's own .gitignore
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	present := map[string]bool{}
	for _, l := range strings.Split(string(existing), "\n") {
		present[strings.TrimSpace(l)] = true
	}
	var add strings.Builder
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		add.WriteString("\n")
	}
	for _, l := range lines {
		if !present[l] {
			add.WriteString(l + "\n")
		}
	}
	if add.Len() == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) //nolint:gosec // the project's own .gitignore
	if err != nil {
		return err
	}
	if _, err := f.WriteString(add.String()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

const trustUsage = "usage: friday trust [--list | --revoke] [--project DIR] [PATH]\n"

// trustCmd manages the per-repository trust store. Without flags it trusts
// PATH (default: the project's .friday/config.toml) at its current content.
func trustCmd(args []string, stdout, stderr io.Writer, environ []string, getwd func() (string, error)) int {
	var list, revoke bool
	var project string
	fs := flag.NewFlagSet("trust", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&list, "list", false, "print every recorded decision")
	fs.BoolVar(&revoke, "revoke", false, "forget the decision for PATH")
	fs.StringVar(&project, "project", "", "project root (default: current directory)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 1 || (list && (revoke || fs.NArg() > 0)) {
		fmt.Fprint(stderr, trustUsage)
		return exitUsage
	}
	store, err := trustStore(environ)
	if err != nil {
		return fail(stderr, "trust", exitError, err)
	}
	if list {
		return trustList(store, stdout, stderr)
	}
	path := fs.Arg(0)
	if path == "" {
		root := project
		if root == "" {
			if root, err = getwd(); err != nil {
				return fail(stderr, "trust", exitError, err)
			}
		}
		path = filepath.Join(root, filepath.FromSlash(config.ProjectConfigRelPath))
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if revoke {
		if err := store.Revoke(path); err != nil {
			return fail(stderr, "trust", exitError, err)
		}
		fmt.Fprintf(stdout, "revoked %s\n", path)
		return exitOK
	}
	data, err := os.ReadFile(path) //nolint:gosec // the owner names the file to trust
	if err != nil {
		return fail(stderr, "trust", exitError, err)
	}
	keys, err := config.GatedKeys(data)
	if err != nil {
		fmt.Fprintf(stderr, "friday trust: %s: %v\n", path, err)
		return exitError
	}
	if len(keys) == 0 {
		fmt.Fprintf(stdout, "%s sets no gated keys (%s); nothing to trust\n", path, gatedText())
		return exitOK
	}
	entry, err := store.Trust(path, data, time.Now())
	if err != nil {
		return fail(stderr, "trust", exitError, err)
	}
	fmt.Fprintf(stdout, "trusted %s (sha256 %s)\n  keys now applied: %s\n", entry.Path, entry.SHA256[:8], strings.Join(keys, ", "))
	return exitOK
}

func trustList(store *config.TrustStore, stdout, stderr io.Writer) int {
	entries, err := store.List()
	if err != nil {
		return fail(stderr, "trust", exitError, err)
	}
	for _, e := range entries {
		fmt.Fprintf(stdout, "%s %s %s\n", e.Decision, e.SHA256[:8], e.Path)
	}
	return exitOK
}

// trustStore resolves the user-state trust file from the environment.
func trustStore(environ []string) (*config.TrustStore, error) {
	path, err := config.TrustStatePath(envLookup(environ))
	if err != nil {
		return nil, err
	}
	return &config.TrustStore{Path: path}, nil
}

// gatedText names the prefixes a repository config may not set untrusted.
func gatedText() string {
	prefixes := make([]string, 0, len(config.ProjectLayerGatedPrefixes))
	for _, p := range config.ProjectLayerGatedPrefixes {
		prefixes = append(prefixes, p+".*")
	}
	return strings.Join(prefixes, ", ")
}

// warnDropped prints one line per repository file and reason naming every
// key that was recorded but not applied, with the command that would apply it.
func warnDropped(stderr io.Writer, r *config.Resolved) {
	type group struct {
		path   string
		reason config.RejectReason
	}
	keys := map[group][]string{}
	for key, chain := range r.Provenance {
		for _, e := range chain {
			if e.Rejected {
				g := group{e.Source.Path, e.Reason}
				keys[g] = append(keys[g], key)
			}
		}
	}
	groups := make([]group, 0, len(keys))
	for g := range keys {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].path < groups[j].path || (groups[i].path == groups[j].path && groups[i].reason < groups[j].reason)
	})
	for _, g := range groups {
		sort.Strings(keys[g])
		remedy := fmt.Sprintf("run `friday trust %s` to apply them", g.path)
		if g.reason == config.RejectAllowlist {
			remedy = "repository files may never set them"
		}
		fmt.Fprintf(stderr, "warning: %s: dropped %s (%s); %s\n", g.path, strings.Join(keys[g], ", "), g.reason, remedy)
	}
}

// trustBlurb states what an answer buys, in the words the gate uses: the
// decision binds to the file's current content, so an edit asks again.
const trustBlurb = "These keys control sandboxing, tools, providers, and telemetry.\nAn answer applies to this exact content: editing the file asks again."

// trustPromptTUI asks the trust question through the same Bubble Tea picker
// the rest of the pre-chat setup uses, so `friday chat` never drops to a raw
// [y/N] line above its own UI. Aborting is the safe answer: no.
func trustPromptTUI(in, out *os.File) config.TrustPrompt {
	return func(path string, keys []string) (config.TrustDecision, error) {
		title := fmt.Sprintf("%s wants to set %s.\n%s\n\nTrust this file?\n", path, strings.Join(keys, ", "), trustBlurb)
		items := []listItem{
			{label: "No", note: "keep the defaults; `friday trust` applies them later"},
			{label: "Yes, trust it", note: "apply these keys"},
		}
		choice, ok, err := selectList(in, out, title, items, 0)
		if err != nil || !ok || choice == 0 {
			return config.TrustUntrusted, nil
		}
		return config.TrustTrusted, nil
	}
}

// trustPrompt is the plain-terminal form, for `friday run`, whose output may
// be a pipe. It reads one byte at a time so the caller keeps the rest of stdin.
func trustPrompt(in io.Reader, out io.Writer) config.TrustPrompt {
	return func(path string, keys []string) (config.TrustDecision, error) {
		fmt.Fprintf(out, "%s sets %s.\n%s\nTrust this file at its current content? [y/N] ", path, strings.Join(keys, ", "), trustBlurb)
		line, err := readLine(in)
		if err != nil && line == "" {
			return config.TrustUntrusted, nil
		}
		if strings.EqualFold(strings.TrimSpace(line), "y") {
			return config.TrustTrusted, nil
		}
		return config.TrustUntrusted, nil
	}
}
