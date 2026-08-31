package tui

import (
	"fmt"
	"regexp"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/lipgloss"

	"github.com/ataidesorg/ink/internal/core"
)

// ThemeColor is one palette slot with a variant per terminal background.
type ThemeColor struct{ Light, Dark string }

func (c ThemeColor) adaptive() lipgloss.AdaptiveColor {
	return lipgloss.AdaptiveColor{Light: c.Light, Dark: c.Dark}
}

// paint is the color a theme actually emits. A fixed palette (Light == Dark)
// uses a complete color so it wins on any terminal background; otherwise it
// follows the terminal's light/dark appearance.
func (c ThemeColor) paint() lipgloss.TerminalColor {
	if c.Light == c.Dark {
		return lipgloss.Color(c.Dark)
	}
	return c.adaptive()
}

// Theme is a named palette the chat styles resolve from at View time.
type Theme struct {
	Name   string
	Label  string     // short picker line; empty for unnamed custom files
	Accent ThemeColor // Ink's voice and the app name
	User   ThemeColor // the human's voice
	OK     ThemeColor
	Warn   ThemeColor
	Fail   ThemeColor
	Dim    ThemeColor
	Rule   ThemeColor
	Bg     ThemeColor // empty = use the terminal background
}

// builtinThemes are always selectable. The first is the default: Ink's
// quiet black palette. Each built-in must keep a distinct accent so
// /theme switching is visible.
func builtinThemes() []Theme {
	hex := func(h string) ThemeColor { return ThemeColor{h, h} }
	ink := Theme{
		Name:  "ink",
		Label: "graphite",
		// Quiet black on the terminal's own field. No canvas: trailing
		// padding would travel with every copied transcript row.
		Accent: hex("#C8C8CC"),
		User:   hex("#F5F5F7"),
		OK:     hex("#30D158"),
		Warn:   hex("#FF9F0A"),
		Fail:   hex("#FF453A"),
		Dim:    hex("#8E8E93"),
		Rule:   hex("#3A3A3C"),
	}
	carbon := Theme{
		Name:   "carbon",
		Label:  "steel",
		Accent: hex("#8FA3B8"),
		User:   hex("#E8EEF4"),
		OK:     hex("#5FB38A"),
		Warn:   hex("#D4A054"),
		Fail:   hex("#E06C75"),
		Dim:    hex("#7B8494"),
		Rule:   hex("#3A4048"),
	}
	sepia := Theme{
		Name:   "sepia",
		Label:  "lamp",
		Accent: hex("#C4A574"),
		User:   hex("#F0E6D4"),
		OK:     hex("#7A9E6A"),
		Warn:   hex("#D19A4A"),
		Fail:   hex("#C45C4A"),
		Dim:    hex("#A09078"),
		Rule:   hex("#4A4038"),
	}
	moss := Theme{
		Name:   "moss",
		Label:  "sage",
		Accent: hex("#7FA887"),
		User:   hex("#E6EEE6"),
		OK:     hex("#5A9A62"),
		Warn:   hex("#C9A227"),
		Fail:   hex("#C65B4A"),
		Dim:    hex("#7E8B7C"),
		Rule:   hex("#3A423A"),
	}
	wine := Theme{
		Name:   "wine",
		Label:  "oxblood",
		Accent: hex("#B5525A"),
		User:   hex("#F2E8E8"),
		OK:     hex("#5F9E72"),
		Warn:   hex("#D4A04A"),
		Fail:   hex("#E05555"),
		Dim:    hex("#9A8084"),
		Rule:   hex("#3E3234"),
	}
	sea := Theme{
		Name:   "sea",
		Label:  "teal",
		Accent: hex("#5EA3A8"),
		User:   hex("#E4EEF0"),
		OK:     hex("#4DA67A"),
		Warn:   hex("#D4A04A"),
		Fail:   hex("#D46565"),
		Dim:    hex("#7A9094"),
		Rule:   hex("#2E3A3C"),
	}
	dark := Theme{
		Name:   "dark",
		Label:  "cool blue",
		Accent: hex("#6CB6FF"),
		User:   hex("#E6EDF3"),
		OK:     hex("#3FB950"),
		Warn:   hex("#D29922"),
		Fail:   hex("#F85149"),
		Dim:    hex("#8B949E"),
		Rule:   hex("#6E7681"),
	}
	light := Theme{
		Name:   "light",
		Label:  "paper",
		Accent: hex("#1D1D1F"),
		User:   hex("#1C1917"),
		OK:     hex("#1A7F37"),
		Warn:   hex("#9A6700"),
		Fail:   hex("#CF222E"),
		Dim:    hex("#57534E"),
		Rule:   hex("#A8A29E"),
		Bg:     hex("#F5F0E8"),
	}
	ansi := Theme{
		Name:   "ansi",
		Label:  "16-color",
		Accent: ThemeColor{"4", "4"},
		User:   ThemeColor{"7", "7"},
		OK:     ThemeColor{"2", "2"},
		Warn:   ThemeColor{"3", "3"},
		Fail:   ThemeColor{"1", "1"},
		Dim:    ThemeColor{"8", "8"},
		Rule:   ThemeColor{"8", "8"},
	}
	return []Theme{ink, carbon, sepia, moss, wine, sea, dark, light, ansi}
}

func defaultTheme() Theme { return builtinThemes()[0] }

// themeFile is the on-disk TOML shape of a custom theme: a variant table per
// background, dark mirroring light (and vice versa) when one is missing.
type themeFile struct {
	Name  string            `toml:"name"`
	Light map[string]string `toml:"light"`
	Dark  map[string]string `toml:"dark"`
}

var themeColorRe = regexp.MustCompile(`^(#[0-9a-fA-F]{6}|[0-9]{1,3})$`)

// ParseTheme decodes one custom theme. name (usually the file's base name)
// is the theme's name unless the file sets its own. Unknown slots and
// malformed colors fail with the offending key, never silently; a slot
// absent from both variants keeps the default theme's color.
func ParseTheme(name string, data []byte) (Theme, error) {
	var f themeFile
	meta, err := toml.Decode(string(data), &f)
	if err != nil {
		return Theme{}, fmt.Errorf("theme %s: %w: %w", name, core.ErrInvalidInput, err)
	}
	if undec := meta.Undecoded(); len(undec) > 0 {
		return Theme{}, fmt.Errorf("%w: theme %s: unknown key %s", core.ErrInvalidInput, name, undec[0].String())
	}
	if f.Name != "" {
		name = f.Name
	}
	t := defaultTheme()
	t.Name = name
	slots := map[string]*ThemeColor{
		"accent": &t.Accent, "user": &t.User, "ok": &t.OK, "warn": &t.Warn,
		"fail": &t.Fail, "dim": &t.Dim, "rule": &t.Rule, "bg": &t.Bg,
	}
	apply := func(variant string, m map[string]string, set func(*ThemeColor, string)) error {
		for k, v := range m {
			slot, ok := slots[k] // strict lowercase: mirroring looks keys up verbatim
			if !ok {
				return fmt.Errorf("%w: theme %s: unknown color %s.%s", core.ErrInvalidInput, name, variant, k)
			}
			if !themeColorRe.MatchString(v) {
				return fmt.Errorf("%w: theme %s: %s.%s = %q is not #RRGGBB or an ANSI number", core.ErrInvalidInput, name, variant, k, v)
			}
			set(slot, v)
		}
		return nil
	}
	if err := apply("light", f.Light, func(c *ThemeColor, v string) { c.Light = v }); err != nil {
		return Theme{}, err
	}
	if err := apply("dark", f.Dark, func(c *ThemeColor, v string) { c.Dark = v }); err != nil {
		return Theme{}, err
	}
	// One variant given mirrors into the other, so a single-palette theme
	// stays that palette on any terminal background.
	for k, slot := range slots {
		_, inLight := f.Light[k]
		_, inDark := f.Dark[k]
		if inLight && !inDark {
			slot.Dark = slot.Light
		}
		if inDark && !inLight {
			slot.Light = slot.Dark
		}
	}
	return t, nil
}
