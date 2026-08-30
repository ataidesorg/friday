package providers

import (
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
)

// TestAllLoadsEveryMatrixRow: the registry parses, stays sorted by id, and
// claims verified status only where a real Ink call earned it.
// TestRegistryMatchesMatrix pins which ids ship.
func TestAllLoadsEveryMatrixRow(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("registry is empty")
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].ID >= all[i].ID {
			t.Fatalf("All() not sorted by id: %q before %q", all[i-1].ID, all[i].ID)
		}
	}
	// verified holds the only entries a real Ink call has passed against;
	// everything else stays unverified (non-negotiable: no fabricated support).
	verified := map[string]bool{
		"fireworks": true, // run 01a034b1-ac87-7b98 completed_verified on kimi-k2p7-code, 2026-08-24
	}
	for _, e := range all {
		want := StatusUnverified
		if verified[e.ID] {
			want = StatusVerified
		}
		if e.Status != want {
			t.Errorf("%s: status %q, want %q (verified only after a real call passes)", e.ID, e.Status, want)
		}
	}
}

func TestNamesAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, e := range All() {
		for _, name := range append([]string{e.ID}, e.Aliases...) {
			if prior, dup := seen[name]; dup {
				t.Errorf("name %q claimed by both %s and %s", name, prior, e.ID)
			}
			seen[name] = e.ID
		}
	}
}

func TestLookupByIDAndAlias(t *testing.T) {
	for _, name := range []string{"fireworks", "fw", "fireworks-ai"} {
		e, ok := Lookup(name)
		if !ok || e.ID != "fireworks" {
			t.Fatalf("Lookup(%q) = %+v, %v; want fireworks entry", name, e, ok)
		}
	}
	if _, ok := Lookup("no-such-provider"); ok {
		t.Fatal("Lookup(no-such-provider) succeeded")
	}
	if _, ok := Lookup(""); ok {
		t.Fatal("Lookup of empty string succeeded")
	}
}

func TestEnumsAreClosed(t *testing.T) {
	wires := map[string]bool{WireChatCompletions: true, WireResponses: true, WireAnthropicMessages: true, WireACP: true, WireVertex: true, WireBedrock: true}
	kinds := map[string]bool{AuthKey: true, AuthOAuth2PKCE: true, AuthOAuth2Device: true, AuthExternalCLI: true, AuthCloud: true, AuthNone: true}
	for _, e := range All() {
		if !wires[e.Wire] {
			t.Errorf("%s: unknown wire %q", e.ID, e.Wire)
		}
		if !kinds[e.Auth.Kind] {
			t.Errorf("%s: unknown auth kind %q", e.ID, e.Auth.Kind)
		}
		switch core.PrivacyClass(e.Privacy) {
		case core.PrivacyLocal, core.PrivacyPrivateCloud, core.PrivacyPublicCloud:
		default:
			t.Errorf("%s: unknown privacy %q", e.ID, e.Privacy)
		}
		if e.Auth.Kind == AuthKey && len(e.Auth.EnvNames) == 0 && !strings.Contains(e.Notes, "config") {
			t.Errorf("%s: key auth with no env names and no config note", e.ID)
		}
	}
}

func TestOptInRiskExactlyTheRuledFlows(t *testing.T) {
	var risky []string
	for _, e := range All() {
		if e.Auth.OptInRisk {
			risky = append(risky, e.ID)
		}
	}
	want := []string{"anthropic-oauth"}
	if len(risky) != 1 || risky[0] != want[0] {
		t.Fatalf("opt-in-risk flows = %v, want exactly %v", risky, want)
	}
}

func TestLocalAndPrivateCloudSets(t *testing.T) {
	wantLocal := map[string]bool{"ollama": true, "lmstudio": true, "copilot-acp": true}
	for _, e := range All() {
		if wantLocal[e.ID] && e.Privacy != string(core.PrivacyLocal) {
			t.Errorf("%s: privacy %q, want local", e.ID, e.Privacy)
		}
	}
}

func TestCopilotEnvPriorityOrder(t *testing.T) {
	e, _ := Lookup("copilot")
	want := []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"}
	if len(e.Auth.EnvNames) != len(want) {
		t.Fatalf("copilot env names = %v, want %v", e.Auth.EnvNames, want)
	}
	for i := range want {
		if e.Auth.EnvNames[i] != want[i] {
			t.Fatalf("copilot env names = %v, want %v (priority order)", e.Auth.EnvNames, want)
		}
	}
}

func TestWireFor(t *testing.T) {
	tests := []struct{ base, override, want string }{
		{"https://api.minimax.io/anthropic", "", WireAnthropicMessages},
		{"https://x.example/anthropic/", "", WireAnthropicMessages},
		{"https://api.fireworks.ai/inference/v1", "", ""},
		{"https://api.x.ai/v1", WireResponses, WireResponses},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := WireFor(tt.base, tt.override); got != tt.want {
			t.Errorf("WireFor(%q, %q) = %q, want %q", tt.base, tt.override, got, tt.want)
		}
	}
}

func TestKnownKind(t *testing.T) {
	for _, kind := range []string{"mock", "openai_compatible", "fireworks", "fw", "ollama"} {
		if err := KnownKind(kind); err != nil {
			t.Errorf("KnownKind(%q) = %v, want nil", kind, err)
		}
	}
	err := KnownKind("firewoks")
	if err == nil {
		t.Fatal("KnownKind(firewoks) = nil, want error")
	}
	if !strings.Contains(err.Error(), "fireworks") {
		t.Errorf("KnownKind(firewoks) error %q lacks the nearest-match hint", err)
	}
	if err := KnownKind(""); err == nil {
		t.Fatal("KnownKind of empty string succeeded")
	}
}

// TestRegistryMatchesMatrix pins the exact id set the registry ships. A row
// added or removed must show up here deliberately.
func TestRegistryMatchesMatrix(t *testing.T) {
	matrix := []string{
		"actual", "anthropic", "anthropic-oauth", "azure-foundry", "bedrock",
		"codex", "copilot", "copilot-acp", "fireworks", "gemini", "lmstudio",
		"nous", "ollama", "openai", "openrouter", "vertex", "xai-oauth",
	}
	want := map[string]bool{}
	for _, id := range matrix {
		want[id] = true
	}
	got := map[string]bool{}
	for _, e := range All() {
		got[e.ID] = true
		if !want[e.ID] {
			t.Errorf("registry entry %q has no matrix row", e.ID)
		}
	}
	for _, id := range matrix {
		if !got[id] {
			t.Errorf("matrix row %q missing from the registry", id)
		}
	}
}

// Key-auth cloud entries the connect wizard offers carry the vendor's
// key-issuing page, and codex carries the recorded chat base URL so a
// sign-in is immediately usable.
func TestKeyURLsAndCodexBaseURL(t *testing.T) {
	for _, id := range []string{"fireworks", "openai", "openrouter", "anthropic"} {
		e, ok := Lookup(id)
		if !ok || !strings.HasPrefix(e.KeyURL, "https://") {
			t.Errorf("%s: key_url %q, want an https page", id, e.KeyURL)
		}
	}
	codex, _ := Lookup("codex")
	if codex.BaseURL == "" {
		t.Fatal("codex has no recorded chat base URL; OAuth sign-in would connect nothing")
	}
}
