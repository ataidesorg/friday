package config

import (
	"fmt"
	"path/filepath"

	"github.com/ataidesorg/ink/internal/core"
)

// File names and locations.
const (
	UserConfigFileName   = "config.toml"
	ProfilesDirName      = "profiles"
	ProjectConfigRelPath = ".ink/config.toml"
	// ProjectLocalConfigRelPath is the git-ignored per-clone layer; same
	// schema and trust rule as the committed file.
	ProjectLocalConfigRelPath = ".ink/config.local.toml"
	configDirEnv              = "INK_CONFIG_DIR"
)

// Dir resolves the user configuration directory:
// $INK_CONFIG_DIR, then $XDG_CONFIG_HOME/ink, then $HOME/.config/ink.
func Dir(getenv func(string) string) (string, error) {
	if dir := getenv(configDirEnv); dir != "" {
		return dir, nil
	}
	if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ink"), nil
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", "ink"), nil
	}
	return "", fmt.Errorf("%w: none of %s, XDG_CONFIG_HOME, HOME is set", core.ErrInvalidInput, configDirEnv)
}

func userConfigPath(configDir string) string {
	return filepath.Join(configDir, UserConfigFileName)
}

func profilePath(configDir, name string) string {
	return filepath.Join(configDir, ProfilesDirName, name+".toml")
}

func projectConfigPath(root string) string {
	return filepath.Join(root, filepath.FromSlash(ProjectConfigRelPath))
}

func projectLocalConfigPath(root string) string {
	return filepath.Join(root, filepath.FromSlash(ProjectLocalConfigRelPath))
}
