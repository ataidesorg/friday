// Package commands loads user-defined slash commands: Markdown prompt files
// under <friday-home>/commands and <project>/.friday/commands whose body is
// the prompt, with optional TOML frontmatter for description and model.
package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/fmatter"
)

// Command is one loadable slash command.
type Command struct {
	Name        string
	Description string
	Model       string // route name to switch to before running; empty keeps the active route
	Body        string // the prompt; $ARGUMENTS expands to the typed arguments
}

// reserved are the chat's built-in slash commands; a file with one of these
// names is skipped so customs can never shadow a built-in.
// reserved must hold every name ChatModel.command dispatches, or a custom
// file would load and then never run.
var reserved = map[string]bool{
	"help": true, "status": true, "copy": true, "export": true,
	"doctor": true, "history": true, "home": true, "rewind": true,
	"fork": true, "rename": true, "delete": true, "always-approve": true,
	"vim-mode": true, "plan": true, "theme": true, "multiline": true,
	"timestamps": true, "usage": true, "tools": true, "thinking": true,
	"dashboard": true, "skills": true, "clear": true,
	"cost": true, "model": true, "agent": true, "new": true, "resume": true,
	"compact": true, "connect": true, "verbose": true, "quit": true,
	"exit": true,
}

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

type frontmatter struct {
	Description string `toml:"description"`
	Model       string `toml:"model"`
}

// Parse builds a Command from one file's content.
func Parse(name string, b []byte) (Command, error) {
	if !validName.MatchString(name) {
		return Command{}, fmt.Errorf("%w: command name %q (want lowercase letters, digits, . _ -)", core.ErrInvalidInput, name)
	}
	if reserved[name] {
		return Command{}, fmt.Errorf("%w: command name %q shadows a built-in", core.ErrConflict, name)
	}
	meta, body, err := fmatter.Split(b)
	if err != nil {
		return Command{}, err
	}
	var fm frontmatter
	if len(meta) > 0 {
		md, err := toml.Decode(string(meta), &fm)
		if err != nil {
			return Command{}, fmt.Errorf("%w: frontmatter: %w", core.ErrInvalidInput, err)
		}
		if u := md.Undecoded(); len(u) > 0 {
			return Command{}, fmt.Errorf("%w: frontmatter key %q is unknown (want description, model)", core.ErrInvalidInput, u[0].String())
		}
	}
	if strings.TrimSpace(body) == "" {
		return Command{}, fmt.Errorf("%w: prompt body is empty", core.ErrInvalidInput)
	}
	return Command{Name: name, Description: fm.Description, Model: fm.Model, Body: strings.TrimSpace(body)}, nil
}

// maxCommandSize bounds one prompt file, mirroring the skills cap.
const maxCommandSize = 256 * 1024

// Load reads every *.md under the user then project command directories,
// name-sorted; a project command replaces a user command with the same name.
// A bad file is reported on warn and skipped; commands never block a launch.
func Load(root, home string, warn io.Writer) []Command {
	byName := map[string]Command{}
	dirs := []string{}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "commands"))
	}
	if root != "" {
		dirs = append(dirs, filepath.Join(root, ".friday", "commands"))
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // no directory is the common case, not a failure
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			if fi, err := e.Info(); err == nil && fi.Size() > maxCommandSize {
				fmt.Fprintf(warn, "friday: command %s skipped: %d bytes (max %d)\n", e.Name(), fi.Size(), maxCommandSize)
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // the user's own command files under known roots
			if err != nil {
				fmt.Fprintf(warn, "friday: command %s skipped: %v\n", e.Name(), err)
				continue
			}
			c, err := Parse(strings.TrimSuffix(e.Name(), ".md"), b)
			if err != nil {
				fmt.Fprintf(warn, "friday: command %s skipped: %v\n", e.Name(), err)
				continue
			}
			byName[c.Name] = c
		}
	}
	out := make([]Command, 0, len(byName))
	for _, c := range byName {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Expand substitutes the typed arguments into the body: $ARGUMENTS when the
// body names it, appended as a trailing paragraph otherwise.
func Expand(c Command, args string) string {
	if strings.Contains(c.Body, "$ARGUMENTS") {
		return strings.ReplaceAll(c.Body, "$ARGUMENTS", args)
	}
	if args == "" {
		return c.Body
	}
	return c.Body + "\n\n" + args
}
