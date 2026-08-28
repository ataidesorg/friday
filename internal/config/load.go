package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Options are the only inputs to Load; nothing is read from the process
// environment implicitly.
type Options struct {
	ConfigDir   string            // user config directory; empty skips user and profile layers
	Profile     string            // profile name; empty uses profile.active
	ProjectRoot string            // directory holding .friday/config.toml; empty skips it
	Environ     []string          // typically os.Environ()
	Overrides   map[string]string // CLI --set key=value pairs
	Trust       *TrustStore       // repository trust decisions; nil treats every repository file as untrusted
	Prompt      TrustPrompt       // asks the owner about an undecided file; nil means no terminal
	Now         func() time.Time  // stamps trust decisions; nil uses time.Now
}

// Resolved is the merged configuration with its history.
type Resolved struct {
	Config     Config
	Merged     map[string]any
	Provenance Provenance
	Sources    []Source // layers that contributed, in LayerOrder
	Unknown    []string // keys no struct field accepts; rejected by Validate

	gate gate
}

// Load merges every layer in LayerOrder. Missing files are fine; unreadable
// or invalid files are errors that name the path.
func Load(opts Options) (*Resolved, error) {
	defaults, err := Defaults()
	if err != nil {
		return nil, err
	}
	r := &Resolved{Provenance: Provenance{}}
	r.apply(defaults, Source{Layer: LayerDefaults})

	if opts.ConfigDir != "" {
		if err := r.applyFile(LayerUser, userConfigPath(opts.ConfigDir)); err != nil {
			return nil, err
		}
		if name := r.profileName(opts.Profile); name != "" {
			if err := r.applyFile(LayerProfile, profilePath(opts.ConfigDir, name)); err != nil {
				return nil, err
			}
		}
	}
	if opts.ProjectRoot != "" {
		now := opts.Now
		if now == nil {
			now = time.Now
		}
		r.gate = gate{store: opts.Trust, prompt: opts.Prompt, now: now}
		if err := r.applyFile(LayerProject, projectConfigPath(opts.ProjectRoot)); err != nil {
			return nil, err
		}
		if err := r.applyFile(LayerProjectLocal, projectLocalConfigPath(opts.ProjectRoot)); err != nil {
			return nil, err
		}
	}
	env, err := parseEnv(opts.Environ)
	if err != nil {
		return nil, err
	}
	r.apply(env, Source{Layer: LayerEnv})

	overrides := map[string]string{}
	for k, v := range opts.Overrides {
		overrides[k] = v
	}
	if opts.Profile != "" {
		overrides["profile.active"] = opts.Profile
	}
	cli, err := overridesToMap(overrides)
	if err != nil {
		return nil, err
	}
	r.apply(cli, Source{Layer: LayerCLI})

	if err := r.decode(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Resolved) apply(layer map[string]any, from Source) {
	if len(layer) == 0 {
		return
	}
	r.Merged = merge(r.Merged, layer, from, r.Provenance, "")
	r.Sources = append(r.Sources, from)
}

func (r *Resolved) applyFile(layer Layer, path string) error {
	data, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s config %s: %w", layer, path, err)
	}
	var m map[string]any
	if _, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&m); err != nil {
		return fmt.Errorf("parse %s config %s: %w", layer, path, err)
	}
	from := Source{Layer: layer, Path: path}
	if layer == LayerProject || layer == LayerProjectLocal {
		// Repository files are untrusted data: the gate keeps the allowlist,
		// records the rest as rejected provenance, and merges the remainder
		// only for a file the owner trusted at this exact content.
		if m, err = r.gate.apply(m, from, data, r.Provenance); err != nil {
			return err
		}
	}
	r.Merged = merge(r.Merged, m, from, r.Provenance, "")
	r.Sources = append(r.Sources, from)
	return nil
}

// projectMaySet reports whether a repository file sets key without trust.
// Everything merges except ProjectLayerGatedPrefixes and project.commands.*,
// which feed the command allowlist and run during verification: executable
// side effects stay behind the trust gate.
func projectMaySet(key string) bool {
	if key == "project.commands" || strings.HasPrefix(key, "project.commands.") {
		return false
	}
	for _, p := range ProjectLayerGatedPrefixes {
		if key == p || strings.HasPrefix(key, p+".") {
			return false
		}
	}
	return true
}

func (r *Resolved) profileName(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v, ok := lookup(r.Merged, "profile.active"); ok {
		name, _ := v.(string)
		return name
	}
	return ""
}

// decode turns the merged map into a Config by round-tripping through TOML,
// which applies struct tags and reports keys nothing accepts.
func (r *Resolved) decode() error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(r.Merged); err != nil {
		return fmt.Errorf("encode merged config: %w", err)
	}
	var c Config
	md, err := toml.NewDecoder(&buf).Decode(&c)
	if err != nil {
		return fmt.Errorf("decode merged config: %w", err)
	}
	r.Config = c
	r.Unknown = leafKeys(md.Undecoded())
	return nil
}

// leafKeys drops table keys that only exist because a child is undecoded,
// so "extra" and "extra.x" report once as "extra.x".
func leafKeys(keys []toml.Key) []string {
	dotted := make([]string, 0, len(keys))
	for _, k := range keys {
		dotted = append(dotted, strings.Join(k, "."))
	}
	leaves := make([]string, 0, len(dotted))
	for _, k := range dotted {
		isParent := false
		for _, other := range dotted {
			if strings.HasPrefix(other, k+".") {
				isParent = true
				break
			}
		}
		if !isParent {
			leaves = append(leaves, k)
		}
	}
	sort.Strings(leaves)
	return leaves
}
