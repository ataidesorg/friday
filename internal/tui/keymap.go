package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ataidesorg/friday/internal/core"
)

// Keymap binds the chat's rebindable actions. Enter, esc, and ctrl+c are
// fixed: submit, close, and cancel/quit are safety rails, not preferences.
type Keymap struct {
	Palette    tea.KeyType // open the command palette
	ScrollUp   tea.KeyType // scrollback one page up
	ScrollDown tea.KeyType // scrollback one page down
}

// DefaultKeymap is the out-of-the-box binding set.
func DefaultKeymap() Keymap {
	return Keymap{Palette: tea.KeyCtrlP, ScrollUp: tea.KeyPgUp, ScrollDown: tea.KeyPgDown}
}

// bindableKeys are the names [tui.keys] may use. ctrl+c, ctrl+m (enter),
// ctrl+i (tab), ctrl+h (a backspace alias), and esc are deliberately absent.
var bindableKeys = map[string]tea.KeyType{
	"ctrl+a": tea.KeyCtrlA, "ctrl+b": tea.KeyCtrlB, "ctrl+d": tea.KeyCtrlD,
	"ctrl+e": tea.KeyCtrlE, "ctrl+f": tea.KeyCtrlF, "ctrl+g": tea.KeyCtrlG,
	"ctrl+j": tea.KeyCtrlJ, "ctrl+k": tea.KeyCtrlK, "ctrl+l": tea.KeyCtrlL,
	"ctrl+n": tea.KeyCtrlN, "ctrl+o": tea.KeyCtrlO, "ctrl+p": tea.KeyCtrlP,
	"ctrl+q": tea.KeyCtrlQ, "ctrl+r": tea.KeyCtrlR, "ctrl+s": tea.KeyCtrlS,
	"ctrl+t": tea.KeyCtrlT, "ctrl+u": tea.KeyCtrlU, "ctrl+v": tea.KeyCtrlV,
	"ctrl+w": tea.KeyCtrlW, "ctrl+x": tea.KeyCtrlX, "ctrl+y": tea.KeyCtrlY,
	"ctrl+z": tea.KeyCtrlZ,
	"pgup":   tea.KeyPgUp, "pgdown": tea.KeyPgDown,
	"home": tea.KeyHome, "end": tea.KeyEnd,
	"f1": tea.KeyF1, "f2": tea.KeyF2, "f3": tea.KeyF3, "f4": tea.KeyF4,
	"f5": tea.KeyF5, "f6": tea.KeyF6, "f7": tea.KeyF7, "f8": tea.KeyF8,
	"f9": tea.KeyF9, "f10": tea.KeyF10, "f11": tea.KeyF11, "f12": tea.KeyF12,
}

// ParseKeymap lays [tui.keys] over the defaults. Unknown actions, unknown or
// reserved keys, and two actions on one key all fail with the offending
// entry named, so a typo never silently keeps a default.
func ParseKeymap(keys map[string]string) (Keymap, error) {
	km := DefaultKeymap()
	slots := map[string]*tea.KeyType{
		"palette":   &km.Palette,
		"scroll_up": &km.ScrollUp, "scroll_down": &km.ScrollDown,
	}
	for action, name := range keys {
		slot, ok := slots[action]
		if !ok {
			known := make([]string, 0, len(slots))
			for k := range slots {
				known = append(known, k)
			}
			sort.Strings(known)
			return Keymap{}, fmt.Errorf("%w: tui.keys.%s: unknown action (have %s)", core.ErrInvalidInput, action, strings.Join(known, ", "))
		}
		kt, ok := bindableKeys[strings.ToLower(name)]
		if !ok {
			return Keymap{}, fmt.Errorf("%w: tui.keys.%s = %q: unknown or reserved key", core.ErrInvalidInput, action, name)
		}
		*slot = kt
	}
	seen := map[tea.KeyType]string{}
	for _, action := range []string{"palette", "scroll_up", "scroll_down"} {
		kt := *slots[action]
		if prev, dup := seen[kt]; dup {
			return Keymap{}, fmt.Errorf("%w: tui.keys: %s and %s are both bound to %s", core.ErrInvalidInput, prev, action, kt.String())
		}
		seen[kt] = action
	}
	return km, nil
}
