package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseKeymapDefaultsAndOverride(t *testing.T) {
	km, err := ParseKeymap(nil)
	if err != nil {
		t.Fatal(err)
	}
	if km != DefaultKeymap() {
		t.Fatalf("empty config changed the defaults: %+v", km)
	}
	km, err = ParseKeymap(map[string]string{"palette": "ctrl+o", "scroll_up": "ctrl+k"})
	if err != nil {
		t.Fatal(err)
	}
	if km.Palette != tea.KeyCtrlO || km.ScrollUp != tea.KeyCtrlK || km.ScrollDown != tea.KeyPgDown {
		t.Fatalf("override wrong: %+v", km)
	}
}

func TestParseKeymapRejects(t *testing.T) {
	cases := map[string]map[string]string{
		"unknown action":        {"jump": "ctrl+o"},
		"unknown key":           {"palette": "hyper+q"},
		"reserved ctrl+c":       {"palette": "ctrl+c"},
		"reserved enter":        {"palette": "ctrl+m"},
		"conflict with default": {"scroll_up": "ctrl+p"},
		"conflict between two":  {"scroll_up": "ctrl+o", "scroll_down": "ctrl+o"},
	}
	for name, keys := range cases {
		if _, err := ParseKeymap(keys); err == nil {
			t.Fatalf("%s: accepted %v", name, keys)
		} else if !strings.Contains(err.Error(), "tui.keys") {
			t.Fatalf("%s: error does not name the setting: %v", name, err)
		}
	}
}
