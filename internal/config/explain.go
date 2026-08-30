package config

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Explanation is the effective value of one key and every layer that set it.
type Explanation struct {
	Key   string
	Value any
	Chain []Entry
}

// Explain reports where a key's value came from. ok is false when no layer
// set the key and it is not in the merged config.
func (r *Resolved) Explain(key string) (Explanation, bool) {
	value, found := lookup(r.Merged, key)
	chain := r.Provenance[key]
	if !found && len(chain) == 0 {
		return Explanation{}, false
	}
	return Explanation{Key: key, Value: value, Chain: slices.Clone(chain)}, true
}

// String renders the key, its effective value, and one line per layer:
//
//	sandbox.provider = "process"
//	  defaults → "process"
//	  project (/repo/.ink/config.toml) → "container"  [rejected: untrusted]
func (e Explanation) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s = %s", e.Key, formatValue(e.Value))
	for _, en := range e.Chain {
		fmt.Fprintf(&b, "\n  %s", en.Source.Layer)
		if en.Source.Path != "" {
			fmt.Fprintf(&b, " (%s)", en.Source.Path)
		}
		fmt.Fprintf(&b, " → %s", formatValue(en.Value))
		if en.Rejected {
			fmt.Fprintf(&b, "  [rejected: %s]", en.Reason)
		}
	}
	return b.String()
}

func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "<unset>"
	case string:
		return strconv.Quote(x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			parts = append(parts, formatValue(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		return fmt.Sprintf("<table, %d keys>", len(x))
	default:
		return fmt.Sprint(x)
	}
}

// TOML renders the effective configuration, comments stripped.
func (r *Resolved) TOML() (string, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(r.Merged); err != nil {
		return "", fmt.Errorf("encode config: %w", err)
	}
	return buf.String(), nil
}
