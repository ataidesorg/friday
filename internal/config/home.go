package config

import (
	"fmt"
	"path/filepath"

	"github.com/ataidesorg/friday/internal/core"
)

const fridayHomeEnv = "FRIDAY_HOME"

// FridayHome resolves the Friday home directory, where sessions, logs, and
// local stores live. Resolution order: $FRIDAY_HOME, then $HOME/.friday, then
// the Friday state directory ($FRIDAY_STATE_DIR or $XDG_STATE_HOME/friday) as
// a fallback when $HOME is unset. It fails closed when none is resolvable.
func FridayHome(getenv func(string) string) (string, error) {
	if dir := getenv(fridayHomeEnv); dir != "" {
		return filepath.Clean(dir), nil
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".friday"), nil
	}
	dir, err := StateFilePath(getenv, "")
	if err != nil {
		return "", fmt.Errorf("%w: cannot resolve Friday home (none of %s, %s, XDG_STATE_HOME, HOME set)",
			core.ErrInvalidInput, fridayHomeEnv, stateDirEnv)
	}
	return filepath.Clean(dir), nil
}
