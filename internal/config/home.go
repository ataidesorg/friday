package config

import (
	"fmt"
	"path/filepath"

	"github.com/ataidesorg/ink/internal/core"
)

const homeEnv = "INK_HOME"

// Home resolves the Ink home directory, where sessions, logs, and
// local stores live. Resolution order: $INK_HOME, then $HOME/.ink, then
// the Ink state directory ($INK_STATE_DIR or $XDG_STATE_HOME/ink) as
// a fallback when $HOME is unset. It fails closed when none is resolvable.
func Home(getenv func(string) string) (string, error) {
	if dir := getenv(homeEnv); dir != "" {
		return filepath.Clean(dir), nil
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".ink"), nil
	}
	dir, err := StateFilePath(getenv, "")
	if err != nil {
		return "", fmt.Errorf("%w: cannot resolve Ink home (none of %s, %s, XDG_STATE_HOME, HOME set)",
			core.ErrInvalidInput, homeEnv, stateDirEnv)
	}
	return filepath.Clean(dir), nil
}
