// Package skills discovers agent skills: skills/<name>/SKILL.md files whose
// frontmatter names and describes a capability and whose body holds the full
// instructions. Agents see only name+description; the skill tool loads the
// body on demand, so skills never bloat the context wholesale.
package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/fmatter"
)

// Skill is one discovered skill.
type Skill struct {
	Name        string
	Description string
	Content     string
	Path        string
}

// maxSkillSize bounds one SKILL.md; anything larger is skipped with a
// warning so a stray binary or generated file never floods a prompt.
const maxSkillSize = 256 * 1024

type frontmatter struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
}

// Load reads every skills/<dir>/SKILL.md under the user home then the
// project root, name-sorted; a project skill replaces a user skill with the
// same name. Bad files are reported on warn and skipped.
func Load(root, home string, warn io.Writer) []Skill {
	byName := map[string]Skill{}
	dirs := []string{}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "skills"))
	}
	if root != "" {
		dirs = append(dirs, filepath.Join(root, "skills"))
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			s, err := parse(filepath.Join(dir, e.Name(), "SKILL.md"), e.Name())
			if err != nil {
				if os.IsNotExist(err) {
					continue // a directory without SKILL.md is not a skill
				}
				fmt.Fprintf(warn, "friday: skill %s skipped: %v\n", e.Name(), err)
				continue
			}
			byName[s.Name] = s
		}
	}
	out := make([]Skill, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func parse(path, dirName string) (Skill, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Skill{}, err
	}
	if fi.Size() > maxSkillSize {
		return Skill{}, fmt.Errorf("%w: SKILL.md is %d bytes (max %d)", core.ErrInvalidInput, fi.Size(), maxSkillSize)
	}
	b, err := os.ReadFile(path) //nolint:gosec // the user's own skill files under known roots
	if err != nil {
		return Skill{}, err
	}
	meta, body, err := fmatter.Split(b)
	if err != nil {
		return Skill{}, err
	}
	var fm frontmatter
	if len(meta) > 0 {
		if _, err := toml.Decode(string(meta), &fm); err != nil {
			return Skill{}, fmt.Errorf("%w: frontmatter: %w", core.ErrInvalidInput, err)
		}
	}
	if fm.Name == "" {
		fm.Name = dirName
	}
	if fm.Description == "" {
		return Skill{}, fmt.Errorf("%w: frontmatter has no description", core.ErrInvalidInput)
	}
	if strings.TrimSpace(body) == "" {
		return Skill{}, fmt.Errorf("%w: skill body is empty", core.ErrInvalidInput)
	}
	return Skill{Name: fm.Name, Description: fm.Description, Content: strings.TrimSpace(body), Path: path}, nil
}

// Tool builds the native skill tool over an already-loaded set: the model
// asks for a skill by name and gets its full content. Content is preloaded
// at launch, so the tool itself never touches the filesystem.
func Tool(list []Skill) core.Tool {
	byName := make(map[string]Skill, len(list))
	names := make([]string, 0, len(list))
	for _, s := range list {
		byName[s.Name] = s
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return skillTool{byName: byName, names: names}
}

type skillTool struct {
	byName map[string]Skill
	names  []string
}

var skillSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "Name of the skill to load."}
  },
  "required": ["name"],
  "additionalProperties": false
}`)

func (t skillTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name:        "skill",
		Description: "Load the full instructions of a named agent skill.",
		Risk:        core.RiskReadOnly,
		InputSchema: skillSchema,
	}
}

func (t skillTool) Invoke(_ context.Context, in core.ToolInput, _ core.ToolContext) (core.ToolOutput, error) {
	var args struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(strings.NewReader(string(in.Arguments)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil || args.Name == "" {
		return core.ToolOutput{}, fmt.Errorf("%w: skill wants {\"name\": ...}", core.ErrInvalidInput)
	}
	s, ok := t.byName[args.Name]
	if !ok {
		return core.ToolOutput{}, fmt.Errorf("%w: unknown skill %q (available: %s)", core.ErrInvalidInput, args.Name, strings.Join(t.names, ", "))
	}
	return core.ToolOutput{
		Content:          fmt.Sprintf("Skill %q:\n\n%s", s.Name, s.Content),
		CapabilitiesUsed: []core.Capability{{Risk: core.RiskReadOnly, Scope: core.ResourceScope{Kind: core.ScopeAny}}},
	}, nil
}
