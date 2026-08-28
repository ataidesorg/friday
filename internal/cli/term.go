package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// readLine reads one line, a byte at a time, so the caller keeps the rest of
// stdin for whatever prompt comes next.
func readLine(in io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := in.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			return b.String(), err
		}
	}
}

// listItem is one selectable row: a label plus an optional dim note shown in
// parentheses (a readiness marker, a provider/model pair).
type listItem struct {
	label, note string
}

// selectList runs a minimal single-column Bubble Tea picker over items and
// returns the chosen index. ok is false when the user aborts (q/esc/ctrl+c),
// in which case nothing was chosen. cursor seeds the starting row.
func selectList(in, out *os.File, title string, items []listItem, cursor int) (int, bool, error) {
	p := tea.NewProgram(listModel{title: title, items: items, cursor: cursor}, tea.WithInput(in), tea.WithOutput(out))
	final, err := p.Run()
	if err != nil {
		return 0, false, err
	}
	fm, ok := final.(listModel)
	if !ok || !fm.chosen {
		return 0, false, nil
	}
	return fm.cursor, true, nil
}

type listModel struct {
	title  string
	items  []listItem
	cursor int
	chosen bool
}

func (m listModel) Init() tea.Cmd { return nil }

func (m listModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case "enter":
		m.chosen = true
		return m, tea.Quit
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m listModel) View() string {
	var b strings.Builder
	b.WriteString(m.title + "\n")
	for i, it := range m.items {
		marker := "  "
		if i == m.cursor {
			marker = "> "
		}
		b.WriteString(marker + it.label)
		if it.note != "" {
			b.WriteString("  (" + it.note + ")")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

const lastCopyFile = "last-copy.txt"

var (
	clipboardCommandName = localClipboardCommandName
	runClipboardCommand  = runLocalClipboardCommand
)

// clipboardCopy writes s to the local clipboard when the platform has a
// trusted helper, otherwise it falls back to OSC 52. It also writes
// <home>/last-copy.txt so SSH/tmux sessions can recover the text.
func clipboardCopy(out io.Writer, home, s string) error {
	if s == "" {
		return fmt.Errorf("empty")
	}
	if name := clipboardCommandName(); name != "" {
		if err := runClipboardCommand(name, s); err == nil {
			writeCopyBackup(home, s)
			return nil
		}
	}
	if _, err := io.WriteString(out, "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(s))+"\a"); err != nil {
		return err
	}
	writeCopyBackup(home, s)
	return nil
}

func localClipboardCommandName() string {
	if runtime.GOOS == "darwin" {
		return "pbcopy"
	}
	return ""
}

func runLocalClipboardCommand(name, s string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := osexec.CommandContext(ctx, name) //nolint:gosec // fixed local clipboard helper, no shell
	cmd.Stdin = strings.NewReader(s)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(out) > 0 {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return fmt.Errorf("%s: %w", name, err)
}

func writeCopyBackup(home, s string) {
	if home == "" {
		return
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(home, lastCopyFile), []byte(s), 0o600)
}

func saveCopyFile(path, s string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(s), 0o600) //nolint:gosec // user-named export path
}

const (
	completeMaxResults = 50
	completeMaxVisited = 2000
)

// fileCompleter feeds the TUI @-attachment typeahead: workspace files whose
// relative path contains the typed fragment. Dot and dependency directories
// are skipped, and the walk is capped so a huge tree never stalls a
// keystroke.
func fileCompleter(root string) func(string) []string {
	skip := map[string]bool{"node_modules": true, "vendor": true, "dist": true, "target": true}
	return func(prefix string) []string {
		prefix = strings.ToLower(prefix)
		var out []string
		visited := 0
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if visited++; visited > completeMaxVisited || len(out) >= completeMaxResults {
				return fs.SkipAll
			}
			if d.IsDir() {
				if p != root && (strings.HasPrefix(d.Name(), ".") || skip[d.Name()]) {
					return fs.SkipDir
				}
				return nil
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return nil
			}
			if prefix == "" || strings.Contains(strings.ToLower(rel), prefix) {
				out = append(out, rel)
			}
			return nil
		})
		sort.Strings(out)
		return out
	}
}

// splitCommand turns one project command string into argv. Single and double
// quotes group words; shell operators are rejected because Friday never runs a
// shell: a command is exactly its argv. ponytail: no escapes inside quotes.
func splitCommand(s string) ([]string, error) {
	var argv []string
	var cur strings.Builder
	var quote rune
	inWord := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case strings.ContainsRune("|&;<>`$()", r):
			return nil, fmt.Errorf("command %q: shell operator %q is not allowed; Friday runs argv, not a shell", s, r)
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				argv = append(argv, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("command %q: unterminated quote", s)
	}
	if inWord {
		argv = append(argv, cur.String())
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("command %q: empty", s)
	}
	return argv, nil
}
