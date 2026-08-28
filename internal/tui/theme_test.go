package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBuiltinThemes(t *testing.T) {
	themes := builtinThemes()
	names := make([]string, len(themes))
	for i, th := range themes {
		names[i] = th.Name
	}
	for _, want := range []string{"friday", "dark", "light", "ansi"} {
		if !strings.Contains(strings.Join(names, " "), want) {
			t.Fatalf("built-ins %v missing %s", names, want)
		}
	}
	// dark and light are fixed: both variants equal.
	for _, th := range themes {
		if th.Name != "dark" && th.Name != "light" {
			continue
		}
		if th.Accent.Light != th.Accent.Dark {
			t.Fatalf("theme %s accent not fixed: %+v", th.Name, th.Accent)
		}
	}
}

func TestParseTheme(t *testing.T) {
	th, err := ParseTheme("solar", []byte("[light]\naccent = \"#AA0000\"\n[dark]\naccent = \"#BB0000\"\ndim = \"8\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if th.Name != "solar" || th.Accent.Light != "#AA0000" || th.Accent.Dark != "#BB0000" {
		t.Fatalf("parsed %+v", th)
	}
	// dim only in dark mirrors to light; user absent keeps the default.
	if th.Dim.Light != "8" || th.Dim.Dark != "8" {
		t.Fatalf("dim not mirrored: %+v", th.Dim)
	}
	if th.User != defaultTheme().User {
		t.Fatalf("unset slot changed: %+v", th.User)
	}
	// The file's own name wins over the filename.
	named, err := ParseTheme("file", []byte("name = \"mine\"\n[light]\naccent = \"#AA0000\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if named.Name != "mine" {
		t.Fatalf("name = %q, want mine", named.Name)
	}
	if named.Accent.Dark != "#AA0000" {
		t.Fatalf("light-only accent not mirrored to dark: %+v", named.Accent)
	}
}

func TestParseThemeRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"bad color":   "[light]\naccent = \"red\"\n",
		"unknown key": "[light]\nglow = \"#AA0000\"\n",
		"unknown top": "sparkle = true\n",
		"bad toml":    "[light\n",
	}
	for name, body := range cases {
		if _, err := ParseTheme("x", []byte(body)); err == nil {
			t.Fatalf("%s: parse accepted %q", name, body)
		}
	}
}

func TestLightThemePaintsCanvas(t *testing.T) {
	var light Theme
	for _, th := range builtinThemes() {
		if th.Name == "light" {
			light = th
		}
	}
	if light.Bg.Light == "" || light.Bg.Dark == "" {
		t.Fatal("light theme must set a background")
	}
	cs := themedStyles(true, light)
	if !cs.hasCanvas {
		t.Fatal("light styles must paint a canvas")
	}
	out := cs.applyCanvas("hi", 8, 3)
	if !strings.Contains(out, "hi") {
		t.Fatalf("canvas dropped content: %q", out)
	}
	if len(strings.Split(out, "\n")) != 3 {
		t.Fatalf("canvas height = %d, want 3", len(strings.Split(out, "\n")))
	}
	dark := themedStyles(true, builtinThemes()[1])
	if dark.hasCanvas {
		t.Fatal("dark theme must not force a canvas")
	}
}

func TestBuiltinThemesAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, th := range builtinThemes() {
		key := th.Accent.Dark + "/" + th.Accent.Light
		if prev, ok := seen[key]; ok {
			t.Fatalf("themes %s and %s share an accent; switching would look like a no-op", prev, th.Name)
		}
		seen[key] = th.Name
	}
}

func TestThemeSwitchChangesPaint(t *testing.T) {
	m := NewChat(Options{Width: 80}, nil)
	next, _ := m.applyTheme("dark")
	m = next.(ChatModel)
	if m.themeName != "dark" || m.styledName != "dark" {
		t.Fatalf("theme=%q styled=%q, want dark", m.themeName, m.styledName)
	}
	next, _ = m.applyTheme("ansi")
	m = next.(ChatModel)
	if m.styledName != "ansi" {
		t.Fatalf("styled=%q, want ansi", m.styledName)
	}
}

func TestThemePickerPreviewsOnMove(t *testing.T) {
	m := NewChat(Options{Width: 80}, nil)
	next, _ := m.openThemes()
	m = next.(ChatModel)
	next, _ = m.Update(keyType(tea.KeyDown))
	m = next.(ChatModel)
	if m.themeName != "friday" {
		t.Fatalf("preview must not persist: themeName=%q", m.themeName)
	}
	if m.styledName == "friday" || m.styledName == "" {
		t.Fatalf("moving the theme cursor must preview another palette, styled=%q", m.styledName)
	}
	next, _ = m.Update(keyType(tea.KeyEsc))
	m = next.(ChatModel)
	if m.styledName != "friday" || m.ov != nil {
		t.Fatalf("esc must restore friday, styled=%q ov=%v", m.styledName, m.ov != nil)
	}
}

func TestChatThemeSwitch(t *testing.T) {
	var saved string
	m := NewChat(Options{
		Width:    80,
		Themes:   []Theme{{Name: "custom", Accent: ThemeColor{"#111111", "#222222"}}},
		SetTheme: func(name string) error { saved = name; return nil },
	}, nil)
	if m.themeName != "friday" {
		t.Fatalf("launch theme %q, want friday", m.themeName)
	}

	// Palette "Switch theme" opens the picker listing built-ins and the custom.
	next, _ := m.Update(keyType(tea.KeyCtrlP))
	next, _ = next.(ChatModel).Update(keyRunes("switch theme"))
	next, _ = next.(ChatModel).Update(enter())
	cm := next.(ChatModel)
	if cm.ov == nil || cm.ov.kind != overlayThemes {
		t.Fatal("palette Switch theme must open the theme picker")
	}
	view := cm.ov.view(newChatStyles(false), 80, 10)
	for _, want := range []string{"friday", "dark", "ansi", "custom"} {
		if !strings.Contains(view, want) {
			t.Fatalf("theme picker missing %s:\n%s", want, view)
		}
	}

	// Filter to the custom theme and commit: applied, persisted, announced.
	next, _ = cm.Update(keyRunes("cust"))
	next, _ = next.(ChatModel).Update(enter())
	cm = next.(ChatModel)
	if cm.themeName != "custom" || saved != "custom" {
		t.Fatalf("theme=%q saved=%q, want custom", cm.themeName, saved)
	}
	if !strings.Contains(strings.Join(cm.Lines, "\n"), "theme custom") {
		t.Fatalf("no confirmation line:\n%v", cm.Lines)
	}
}

func TestChatLaunchesOnSavedTheme(t *testing.T) {
	m := NewChat(Options{Width: 80, ThemeName: "ansi"}, nil)
	if m.themeName != "ansi" {
		t.Fatalf("launch theme %q, want ansi", m.themeName)
	}
	// An unknown saved name falls back to the default instead of failing.
	m = NewChat(Options{Width: 80, ThemeName: "gone"}, nil)
	if m.themeName != "friday" {
		t.Fatalf("unknown theme fell back to %q, want friday", m.themeName)
	}
}

func TestMergeThemesShadowsBuiltins(t *testing.T) {
	out := mergeThemes(builtinThemes(), []Theme{{Name: "dark", Accent: ThemeColor{"#000001", "#000001"}}, {Name: "extra"}})
	if len(out) != len(builtinThemes())+1 {
		t.Fatalf("merge produced %d themes", len(out))
	}
	for _, th := range out {
		if th.Name == "dark" && th.Accent.Light != "#000001" {
			t.Fatal("custom dark did not shadow the built-in")
		}
	}
}

func TestParseThemeRejectsUppercaseKeys(t *testing.T) {
	_, err := ParseTheme("x", []byte("[light]\nACCENT = \"#aa0000\"\n"))
	if err == nil || !strings.Contains(err.Error(), "ACCENT") {
		t.Fatalf("uppercase key not rejected with its name: %v", err)
	}
}
