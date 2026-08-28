package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	home := t.TempDir()
	if got := loadSettings(home); got.Theme != "" {
		t.Fatalf("fresh home returned %+v", got)
	}
	if err := saveSettings(home, settings{Theme: "ansi"}); err != nil {
		t.Fatal(err)
	}
	if got := loadSettings(home).Theme; got != "ansi" {
		t.Fatalf("round trip theme %q, want ansi", got)
	}
	fi, err := os.Stat(settingsPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode %v, want 0600", fi.Mode().Perm())
	}
	// Corrupt state is a fresh start, not a failure.
	if err := os.WriteFile(settingsPath(home), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadSettings(home); got.Theme != "" {
		t.Fatalf("corrupt settings returned %+v", got)
	}
}

func TestLoadThemes(t *testing.T) {
	home := t.TempDir()
	if got := loadThemes(home, os.Stderr); got != nil {
		t.Fatalf("no themes dir returned %v", got)
	}
	dir := filepath.Join(home, "themes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTheme := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeTheme("solar.toml", "[light]\naccent = \"#AA0000\"\n")
	writeTheme("broken.toml", "[light]\naccent = \"nope\"\n")
	writeTheme("notes.txt", "not a theme")

	var warn strings.Builder
	themes := loadThemes(home, &warn)
	if len(themes) != 1 || themes[0].Name != "solar" {
		t.Fatalf("loaded %+v, want just solar", themes)
	}
	if !strings.Contains(warn.String(), "broken") {
		t.Fatalf("bad theme not reported: %q", warn.String())
	}
}

func TestApplyEnvFileSetsMissingOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "env")
	body := "# comment\nexport FRIDAY_TEST_A=alpha\nFRIDAY_TEST_B=\"beta value\"\nFRIDAY_TEST_C='gamma'\ninvalid\n=emptyname\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{"FRIDAY_TEST_B": "keep"}
	set := func(k, v string) error { got[k] = v; return nil }
	get := func(k string) string { return got[k] }
	if err := applyEnvFile(p, set, get); err != nil {
		t.Fatal(err)
	}
	if got["FRIDAY_TEST_A"] != "alpha" {
		t.Errorf("A=%q", got["FRIDAY_TEST_A"])
	}
	if got["FRIDAY_TEST_B"] != "keep" {
		t.Errorf("existing must win, B=%q", got["FRIDAY_TEST_B"])
	}
	if got["FRIDAY_TEST_C"] != "gamma" {
		t.Errorf("C=%q", got["FRIDAY_TEST_C"])
	}
}

func TestApplyEnvFileMissingIsOK(t *testing.T) {
	if err := applyEnvFile(filepath.Join(t.TempDir(), "nope"), func(string, string) error { return nil }, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}
