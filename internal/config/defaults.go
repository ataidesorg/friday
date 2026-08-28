package config

import (
	"bytes"
	_ "embed"
	"fmt"

	"github.com/BurntSushi/toml"
)

//go:embed defaults.toml
var defaultsTOML []byte

// Defaults parses the embedded defaults.toml into a fresh generic map.
func Defaults() (map[string]any, error) {
	var m map[string]any
	if _, err := toml.NewDecoder(bytes.NewReader(defaultsTOML)).Decode(&m); err != nil {
		return nil, fmt.Errorf("parse embedded defaults: %w", err)
	}
	return m, nil
}

// DefaultConfig decodes the embedded defaults into a Config.
func DefaultConfig() (Config, error) {
	var c Config
	md, err := toml.NewDecoder(bytes.NewReader(defaultsTOML)).Decode(&c)
	if err != nil {
		return Config{}, fmt.Errorf("decode embedded defaults: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return Config{}, fmt.Errorf("embedded defaults contain unknown keys: %v", undecoded)
	}
	return c, nil
}
