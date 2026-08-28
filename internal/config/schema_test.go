package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/core"
)

func TestDefaultsDecode(t *testing.T) {
	c, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.SchemaVersion != 1 || c.Sandbox.Provider != "process" || c.Tools.DefaultEffect != "deny" {
		t.Fatalf("defaults not closed: %+v", c)
	}
	if len(c.Providers) != 0 || len(c.Models.Routes) != 0 {
		t.Fatalf("defaults must have no providers or routes: %+v", c)
	}
	if !reflect.DeepEqual(c.Tools.Allow, []string{"read_file", "list_dir", "search", "ask_user_question", "todo_write"}) || !reflect.DeepEqual(c.Tools.RequireApproval, []string{"write_file", "apply_patch", "run_command"}) || c.Telemetry.Mode != "local" || c.Evals.Gate != "required" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	m, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	m["sandbox"].(map[string]any)["provider"] = "open"
	if again, _ := Defaults(); again["sandbox"].(map[string]any)["provider"] != "process" {
		t.Fatal("Defaults must return a fresh map each call")
	}
	if len(m["providers"].(map[string]any)) != 0 {
		t.Fatal("defaults.toml must not define providers or auth tables")
	}
}

func TestEveryFieldHasDefault(t *testing.T) {
	m, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	var walk func(typ reflect.Type, node map[string]any, prefix string)
	walk = func(typ reflect.Type, node map[string]any, prefix string) {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			key := strings.Split(f.Tag.Get("toml"), ",")[0]
			val, ok := node[key]
			if !ok {
				t.Errorf("defaults.toml missing key %s%s", prefix, key)
				continue
			}
			if f.Type.Kind() == reflect.Struct {
				sub, isMap := val.(map[string]any)
				if !isMap {
					t.Errorf("%s%s should be a table", prefix, key)
					continue
				}
				walk(f.Type, sub, prefix+key+".")
			}
		}
	}
	walk(reflect.TypeOf(Config{}), m, "")
}

func TestToAgent(t *testing.T) {
	c, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	code := c.Profiles["default"].ToAgent("default")
	if code.Harness != core.HarnessCode || code.MemoryNamespace != "project" || code.SensitivityCap != core.SensitivityInternal {
		t.Fatalf("default %+v", code)
	}
	asst := c.Profiles["assistant"].ToAgent("assistant")
	want := core.DefaultAssistantProfile()
	if asst.Harness != want.Harness || asst.MemoryNamespace != want.MemoryNamespace || asst.SensitivityCap != want.SensitivityCap {
		t.Fatalf("assistant %+v want %+v", asst, want)
	}
	if asst.Style != core.StyleConcise || asst.Posture != core.PostureStrict {
		t.Fatalf("assistant style/posture %+v", asst)
	}
}

func TestToCore(t *testing.T) {
	c, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.ToCore()
	if err != nil {
		t.Fatal(err)
	}
	if v.Budget.MaxCost != 2_000_000 || v.Limits.WallClock != 600*time.Second || v.Privacy != core.PrivacyStandard {
		t.Fatalf("ToCore = %+v", v)
	}
	if v.Limits != (core.ResourceLimits{CPUCores: 1, MemoryMB: 2048, DiskMB: 1024, MaxProcesses: 64, WallClock: 10 * time.Minute}) {
		t.Fatalf("limits = %+v", v.Limits)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Telemetry.Privacy = "loud" },
		func(c *Config) { c.Budgets.PerTaskUSD = -1 },
	} {
		bad := c
		mutate(&bad)
		if _, err := bad.ToCore(); err == nil {
			t.Errorf("ToCore accepted %+v", bad)
		}
	}
}

func TestProviderAuthRefs(t *testing.T) {
	fb := []AuthRef{{Source: "keyring", Service: "friday", Account: "alt"}}
	cases := []struct {
		name string
		p    ProviderConfig
		want int
	}{
		{"no auth", ProviderConfig{}, 0},
		{"primary only", ProviderConfig{Auth: &AuthRef{Source: "env", Name: "K"}}, 1},
		{"primary plus fallback", ProviderConfig{Auth: &AuthRef{Source: "env", Name: "K"}, AuthFallbacks: fb}, 2},
		{"fallbacks without primary", ProviderConfig{AuthFallbacks: fb}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			refs := c.p.AuthRefs()
			if len(refs) != c.want {
				t.Fatalf("AuthRefs() = %d refs, want %d", len(refs), c.want)
			}
			if c.p.Auth != nil && refs[0].Name != c.p.Auth.Name {
				t.Fatal("primary auth must come first in rotation order")
			}
		})
	}
}
