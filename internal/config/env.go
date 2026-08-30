package config

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/ataidesorg/ink/internal/core"
)

// EnvPrefix marks environment overrides: INK__SANDBOX__NETWORK=allowlist.
const EnvPrefix = "INK__"

const keySeparator = "__"

func errKeyConflict(key string) error {
	return fmt.Errorf("%w: key %q is set both as a value and as a table", core.ErrInvalidInput, key)
}

// splitKey splits a dotted key into its segments.
func splitKey(key string) []string { return strings.Split(key, ".") }

// validSegments rejects empty segments such as INK____X or "a..b".
func validSegments(parts []string, raw string) error {
	for _, p := range parts {
		if p == "" {
			return fmt.Errorf("%w: empty key segment in %q", core.ErrInvalidInput, raw)
		}
	}
	return nil
}

// parseEnv turns INK__A__B=value pairs into a nested map. Keys are
// lowercased; values are parsed as TOML scalars or arrays, else kept as
// strings. Variables without the prefix are ignored.
func parseEnv(environ []string) (map[string]any, error) {
	out := map[string]any{}
	for _, kv := range environ {
		name, value, found := strings.Cut(kv, "=")
		if !found || !strings.HasPrefix(name, EnvPrefix) {
			continue
		}
		parts := strings.Split(strings.ToLower(strings.TrimPrefix(name, EnvPrefix)), keySeparator)
		if err := validSegments(parts, name); err != nil {
			return nil, err
		}
		next, err := setNested(out, parts, ParseValue(value))
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", name, err)
		}
		out = next
	}
	return out, nil
}

// ParseValue interprets s as a TOML value (int, float, bool, string, array,
// datetime); anything that does not parse is returned as a plain string.
func ParseValue(s string) any {
	var m map[string]any
	if _, err := toml.Decode("v = "+s, &m); err == nil {
		if v, ok := m["v"]; ok {
			return v
		}
	}
	return s
}

// overridesToMap nests dotted CLI keys (sandbox.provider=container).
func overridesToMap(overrides map[string]string) (map[string]any, error) {
	out := map[string]any{}
	for key, value := range overrides {
		parts := splitKey(key)
		if err := validSegments(parts, key); err != nil {
			return nil, err
		}
		next, err := setNested(out, parts, ParseValue(value))
		if err != nil {
			return nil, fmt.Errorf("override %s: %w", key, err)
		}
		out = next
	}
	return out, nil
}
