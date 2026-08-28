package cli

// End-to-end coverage of the no---script path: a real openai_compatible
// provider served by httptest over SSE, the spy-redactor sweep (a resolved
// token never lands in .friday/local/**, the trail, or `friday trace`
// output), and the unreachable-endpoint health diagnosis.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ataidesorg/friday/internal/config"
	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/providers"
	"github.com/ataidesorg/friday/internal/redact"
)

// spySecret is the credential the spy-redactor test plants; any file or
// output that contains it is a leak.
const spySecret = "sk-spy-secret-4f9c2e8b7a6d5041" //nolint:gosec // planted test fixture, not a real credential gitleaks:allow

const farewellGo = `package sample

import "fmt"

// Farewell returns a goodbye for name.
func Farewell(name string) string {
	if name == "" {
		name = "world"
	}
	return fmt.Sprintf("Goodbye, %s!", name)
}
`

const farewellTestGo = `package sample

import "testing"

func TestFarewell(t *testing.T) {
	cases := map[string]string{"": "Goodbye, world!", "Friday": "Goodbye, Friday!"}
	for in, want := range cases {
		if got := Farewell(in); got != want {
			t.Errorf("Farewell(%q) = %q, want %q", in, got, want)
		}
	}
}
`

type sseCall struct {
	id, name string
	args     map[string]any
}

type sseTurn struct {
	content string
	finish  string
	in, out int
	calls   []sseCall
}

// farewellTurns mirrors test/scripts/add-farewell.json so the real-wire
// run exercises the same conversation verified against the mock.
var farewellTurns = []sseTurn{
	{"I will read greet.go to mirror its style.", "tool_calls", 420, 32,
		[]sseCall{{"call-1", "read_file", map[string]any{"path": "greet.go"}}}},
	{"Adding Farewell next to Greet.", "tool_calls", 610, 140, []sseCall{
		{"call-2", "write_file", map[string]any{"path": "farewell.go", "content": farewellGo}},
		{"call-3", "write_file", map[string]any{"path": "farewell_test.go", "content": farewellTestGo}},
	}},
	{"Running the test suite.", "tool_calls", 700, 24,
		[]sseCall{{"call-4", "run_command", map[string]any{"argv": []string{"go", "test", "./..."}}}}},
	{"Added Farewell(name) in farewell.go with TestFarewell; go test ./... passes.\nLearned: this project keeps one exported function per file with a matching _test.go.", "stop", 760, 48, nil},
}

func sseChunk(t *testing.T, w io.Writer, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
}

// chatServer speaks the chat-completions SSE wire, replaying turns in
// request order and rejecting any request that does not carry the secret.
func chatServer(t *testing.T, secret string, turns []sseTurn) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Errorf("request %d authorization = %q, want the bearer secret", i, got)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("request %d path = %q", i, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if i >= len(turns) {
			t.Errorf("unexpected request %d: script exhausted", i)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		turn := turns[i]
		w.Header().Set("Content-Type", "text/event-stream")
		sseChunk(t, w, map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"role": "assistant", "content": turn.content},
		}}})
		for ci, c := range turn.calls {
			args, err := json.Marshal(c.args)
			if err != nil {
				t.Fatal(err)
			}
			sseChunk(t, w, map[string]any{"choices": []any{map[string]any{
				"delta": map[string]any{"tool_calls": []any{map[string]any{
					"index":    ci,
					"id":       c.id,
					"function": map[string]any{"name": c.name, "arguments": string(args)},
				}}},
			}}})
		}
		sseChunk(t, w, map[string]any{
			"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": turn.finish}},
			"usage":   map[string]any{"prompt_tokens": turn.in, "completion_tokens": turn.out},
		})
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// providerConfigDir writes a user-layer config wiring one openai_compatible
// provider (env-key auth) into the default route with pricing.
func providerConfigDir(t *testing.T, baseURL string) string {
	t.Helper()
	dir := t.TempDir()
	conf := fmt.Sprintf(`[providers.scripted]
kind = "openai_compatible"
base_url = %q
privacy = "local"
auth = { source = "env", name = "SCRIPTED_KEY" }

[models.routes.default]
provider = "scripted"
model = "test-model-1"

[models.routing]
default = "default"

[models.pricing."test-model-1"]
input_usd_per_mtok = 1.0
output_usd_per_mtok = 2.0
`, baseURL)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// execReal invokes the CLI with a caller-owned HOME plus extra environment
// entries (the provider key).
func execReal(t *testing.T, home string, extra []string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	environ := append([]string{"HOME=" + home, "PATH=/usr/bin"}, extra...)
	code := Run(args, &stdout, &stderr, strings.NewReader(""), environ, func() (string, error) { return t.TempDir(), nil })
	return code, stdout.String(), stderr.String()
}

// rawTrail returns the run id and the raw bytes of the single trail.
func rawTrail(t *testing.T, root string) (string, string) {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(root, ".friday", "local", "runs", "*", "events.jsonl"))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("want one trail, got %v (err %v)", dirs, err)
	}
	b, err := os.ReadFile(dirs[0]) //nolint:gosec // path under the test root
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Base(filepath.Dir(dirs[0])), string(b)
}

// scanLocalForSecret walks .friday/local/** and fails on any file whose
// bytes contain the secret.
func scanLocalForSecret(t *testing.T, root, secret string) {
	t.Helper()
	local := filepath.Join(root, ".friday", "local")
	err := filepath.WalkDir(local, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p) //nolint:gosec // path under the test root
		if err != nil {
			return err
		}
		if bytes.Contains(b, []byte(secret)) {
			t.Errorf("credential leaked into %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunRealProviderVerified(t *testing.T) {
	srv, requests := chatServer(t, spySecret, farewellTurns)
	root := copyFixture(t)
	home := t.TempDir()
	cfgDir := providerConfigDir(t, srv.URL)
	trustProject(t, home, root)

	env := []string{"SCRIPTED_KEY=" + spySecret}
	code, out, errOut := execReal(t, home, env,
		"run", "--project", root, "--config-dir", cfgDir, "--no-tui", "--yes",
		"add Farewell(name) with a test")
	if code != exitOK || !strings.Contains(out, "completed_verified") {
		t.Fatalf("run: %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if got := int(requests.Load()); got != len(farewellTurns) {
		t.Errorf("provider served %d requests, want %d", got, len(farewellTurns))
	}

	runID, raw := rawTrail(t, root)
	for _, want := range []string{
		`"model_selected"`, `"route":"default"`, `"provider":"scripted"`, `"model":"test-model-1"`,
		`"model_usage"`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("trail missing %s", want)
		}
	}

	// Spy-redactor sweep: the resolved token must not appear anywhere under
	// .friday/local/** nor in the trace replay of the run.
	scanLocalForSecret(t, root, spySecret)
	tcode, tout, terr := execReal(t, home, env, "trace", "--project", root, runID)
	if tcode != exitOK {
		t.Fatalf("trace: %d\nstdout: %s\nstderr: %s", tcode, tout, terr)
	}
	if strings.Contains(tout, spySecret) || strings.Contains(terr, spySecret) {
		t.Error("credential leaked into trace output")
	}
}

func TestRunUnreachableProviderFails(t *testing.T) {
	// A just-released loopback port refuses connections immediately.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	root := copyFixture(t)
	home := t.TempDir()
	cfgDir := providerConfigDir(t, "http://"+addr)
	trustProject(t, home, root)

	code, out, errOut := execReal(t, home, []string{"SCRIPTED_KEY=" + spySecret},
		"run", "--project", root, "--config-dir", cfgDir, "--no-tui", "--yes", "do it")
	if code != exitFailed {
		t.Fatalf("run: %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if !strings.Contains(errOut, "provider scripted health: unhealthy") || !strings.Contains(errOut, "unreachable") {
		t.Errorf("stderr lacks the health diagnosis: %s", errOut)
	}
	_, raw := rawTrail(t, root)
	if !strings.Contains(raw, "provider_error") {
		t.Error("trail lacks the provider_error outcome")
	}
	if !strings.Contains(raw, "unhealthy") {
		t.Error("trail lacks the health warning")
	}
}

// TestBuildTargetCopilot asserts the copilot decoration on the run path:
// the chat request carries the client-identification headers, the bearer
// comes from the exchange endpoint (never the GitHub token), and a revoked
// bearer is dropped and re-minted exactly once on 401.
func TestBuildTargetCopilot(t *testing.T) {
	ghToken := "gho_spy_run_1234567890" //nolint:gosec // planted test value
	bearer := "spy-run-bearer-1"        //nolint:gosec // planted test value
	var exchanges atomic.Int64
	exch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges.Add(1)
		if got := r.Header.Get("Authorization"); got != "token "+ghToken {
			t.Errorf("exchange auth = %q", got)
		}
		fmt.Fprintf(w, `{"token":%q}`, bearer)
	}))
	defer exch.Close()

	var chats atomic.Int64
	type seenHeaders struct {
		auth, editor, integration, intent, initiator string
	}
	var mu sync.Mutex
	var seen []seenHeaders
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, seenHeaders{
			auth:        r.Header.Get("Authorization"),
			editor:      r.Header.Get("Editor-Version"),
			integration: r.Header.Get("Copilot-Integration-Id"),
			intent:      r.Header.Get("Openai-Intent"),
			initiator:   r.Header.Get("X-Initiator"),
		})
		mu.Unlock()
		if chats.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized) // bearer revoked server-side
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		sseChunk(t, w, map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"role": "assistant", "content": "done"},
		}}})
		sseChunk(t, w, map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{}, "finish_reason": "stop",
		}}})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer chat.Close()

	cfg := config.Config{Providers: map[string]config.ProviderConfig{
		"copilot": {
			BaseURL: chat.URL,
			OAuth:   &config.OAuthRef{ExchangeURL: exch.URL},
		},
	}}
	decision := core.RouteDecision{Selected: core.ModelRoute{Provider: "copilot", Model: "gpt-test"}}
	target, err := buildTarget(decision, "", cfg, redact.New(), []string{"COPILOT_GITHUB_TOKEN=" + ghToken})
	if err != nil {
		t.Fatal(err)
	}
	defer target.creds.close()

	resp, err := target.provider.Complete(context.Background(), core.CompletionRequest{
		Model:    "gpt-test",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil || resp.Content != "done" {
		t.Fatalf("complete: %v %+v", err, resp)
	}
	if exchanges.Load() != 2 {
		t.Fatalf("401 must drop the bearer and re-mint once, exchanges = %d", exchanges.Load())
	}
	warns := target.warns.drain()
	if len(warns) != 1 || !strings.Contains(warns[0], "401") {
		t.Fatalf("buffered trail warnings = %q, want one mentioning 401", warns)
	}
	if len(seen) != 2 {
		t.Fatalf("chat requests = %d", len(seen))
	}
	for i, h := range seen {
		if h.auth != "Bearer "+bearer {
			t.Errorf("request %d auth = %q; must use the exchanged bearer, never the GitHub token", i, h.auth)
		}
		if h.editor == "" || h.integration != "vscode-chat" || h.intent == "" || h.initiator == "" {
			t.Errorf("request %d missing copilot client headers: %+v", i, h)
		}
	}
}

// TestBuildTargetRiskOptIn: vendor-unsanctioned providers fail closed at
// construction without the user-layer opt-in, and reach auth resolution
// with it. Construction covers run and its diagnostic probe.
func TestBuildTargetRiskOptIn(t *testing.T) {
	decision := core.RouteDecision{Selected: core.ModelRoute{Provider: "anthropic-oauth", Model: "claude-test"}}
	env := []string{"ANTHROPIC_TOKEN=spy-risk-token-1"}

	_, err := buildTarget(decision, "", config.Config{}, redact.New(), env)
	if err == nil || !strings.Contains(err.Error(), "accept_third_party_oauth_risk") {
		t.Fatalf("flag unset must fail naming the flag: %v", err)
	}

	cfg := config.Config{Providers: map[string]config.ProviderConfig{
		"anthropic-oauth": {AcceptThirdPartyOAuthRisk: true},
	}}
	target, err := buildTarget(decision, "", cfg, redact.New(), env)
	if err != nil {
		t.Fatal(err)
	}
	defer target.creds.close()
	cred, err := target.creds.credential(context.Background())
	if err != nil || string(cred.Value()) != "spy-risk-token-1" {
		t.Fatalf("opted-in provider must reach auth resolution: %v", err)
	}
}

// TestBuildTargetBedrock: the registry bedrock provider rides the Converse
// wire with per-request SigV4 signing — no bearer, region from AWS_REGION,
// base URL from BEDROCK_BASE_URL.
func TestBuildTargetBedrock(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotAuth, gotDate, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotDate = r.Header.Get("X-Amz-Date")
		gotToken = r.Header.Get("X-Amz-Security-Token")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"from bedrock"}]}},"stopReason":"end_turn","usage":{"inputTokens":7,"outputTokens":3}}`)
	}))
	defer srv.Close()

	secret := "spy-bedrock-secret-key-0001" //nolint:gosec // planted test value; gitleaks:allow
	decision := core.RouteDecision{Selected: core.ModelRoute{Provider: "bedrock", Model: "test-model"}}
	env := []string{
		"BEDROCK_BASE_URL=" + srv.URL,
		"AWS_REGION=us-test-1",
		"AWS_ACCESS_KEY_ID=AKIABUILDTARGET00001", // gitleaks:allow
		"AWS_SECRET_ACCESS_KEY=" + secret,
	}
	target, err := buildTarget(decision, "", config.Config{}, redact.New(), env)
	if err != nil {
		t.Fatal(err)
	}
	defer target.creds.close()

	resp, err := target.provider.Complete(context.Background(), core.CompletionRequest{
		Model:    "test-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "from bedrock" || resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 {
		t.Errorf("resp = %+v", resp)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/model/test-model/converse" {
		t.Errorf("path = %q", gotPath)
	}
	sigRe := regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=AKIABUILDTARGET00001/\d{8}/us-test-1/bedrock/aws4_request, SignedHeaders=[a-z0-9;.-]+, Signature=[0-9a-f]{64}$`)
	if !sigRe.MatchString(gotAuth) {
		t.Errorf("authorization = %q, want SigV4 for us-test-1/bedrock", gotAuth)
	}
	if gotDate == "" {
		t.Error("X-Amz-Date missing")
	}
	if gotToken != "" {
		t.Errorf("X-Amz-Security-Token = %q, want none without a session token", gotToken)
	}
	if strings.Contains(gotAuth, secret) {
		t.Error("authorization header leaks the secret key")
	}
}

// TestBuildTargetBedrockNoRegion: without AWS_REGION and with a base URL the
// region cannot be parsed from, buildTarget fails closed naming the fix.
func TestBuildTargetBedrockNoRegion(t *testing.T) {
	decision := core.RouteDecision{Selected: core.ModelRoute{Provider: "bedrock", Model: "test-model"}}
	_, err := buildTarget(decision, "", config.Config{}, redact.New(), []string{
		"BEDROCK_BASE_URL=http://127.0.0.1:1",
	})
	if err == nil || !strings.Contains(err.Error(), "AWS_REGION") {
		t.Errorf("err = %v, want a miss naming AWS_REGION", err)
	}
}

// TestBuildTargetVertexNeedsBaseURL: vertex has no default endpoint (the
// URL embeds project and region), so a missing VERTEX_BASE_URL fails closed
// with the full endpoint shape in the error.
func TestBuildTargetVertexNeedsBaseURL(t *testing.T) {
	decision := core.RouteDecision{Selected: core.ModelRoute{Provider: "vertex", Model: "gemini-test"}}
	_, err := buildTarget(decision, "", config.Config{}, redact.New(), nil)
	if err == nil {
		t.Fatal("want base-URL error")
	}
	for _, want := range []string{"VERTEX_BASE_URL", "aiplatform.googleapis.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q lacks %q", err, want)
		}
	}
}

func TestAWSRegionFor(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		env     map[string]string
		want    string
		wantErr string
	}{
		{name: "env wins over url", baseURL: "https://bedrock-runtime.eu-west-3.amazonaws.com", env: map[string]string{"AWS_REGION": "us-test-1"}, want: "us-test-1"},
		{name: "default region fallback", env: map[string]string{"AWS_DEFAULT_REGION": "ap-south-1"}, want: "ap-south-1"},
		{name: "parsed from url", baseURL: "https://bedrock-runtime.eu-west-3.amazonaws.com", want: "eu-west-3"},
		{name: "unparseable url", baseURL: "http://127.0.0.1:1", wantErr: "AWS_REGION"},
		{name: "nothing anywhere", wantErr: "BEDROCK_BASE_URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := awsRegionFor(tc.baseURL, func(k string) (string, bool) {
				v, ok := tc.env[k]
				return v, ok
			})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("region = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildTargetKeylessPublicCloudWarns: a keyless public-cloud
// provider works once config confirms an endpoint, sends no Authorization
// header, and warns loudly (stderr + Warning trail event via the buffer).
func TestBuildTargetKeylessPublicCloudWarns(t *testing.T) {
	var mu sync.Mutex
	var gotAuth string
	auths := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		auths++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		sseChunk(t, w, map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"role": "assistant", "content": "anon"},
		}}})
		sseChunk(t, w, map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{}, "finish_reason": "stop",
		}}})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := config.Config{Providers: map[string]config.ProviderConfig{
		"free-cloud": {BaseURL: srv.URL},
	}}
	decision := core.RouteDecision{Selected: core.ModelRoute{Provider: "free-cloud", Model: "free-model"}}
	target, err := buildTarget(decision, "", cfg, redact.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer target.creds.close()

	warned := target.warns.drain()
	found := false
	for _, w := range warned {
		if strings.Contains(w, "keyless") && strings.Contains(w, "unauthenticated") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %q, want the keyless public-endpoint warning", warned)
	}

	resp, err := target.provider.Complete(context.Background(), core.CompletionRequest{
		Model:    "free-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "anon" {
		t.Errorf("content = %q", resp.Content)
	}
	mu.Lock()
	defer mu.Unlock()
	if auths == 0 || gotAuth != "" {
		t.Errorf("Authorization = %q over %d requests, want none sent", gotAuth, auths)
	}
}

// TestBuildTargetKeylessUnconfirmedEndpoint: a keyless provider with no
// confirmed endpoint stays unusable until a config base_url names one, and
// the error names the fix.
func TestBuildTargetKeylessUnconfirmedEndpoint(t *testing.T) {
	cfg := config.Config{Providers: map[string]config.ProviderConfig{"free-cloud": {}}}
	decision := core.RouteDecision{Selected: core.ModelRoute{Provider: "free-cloud", Model: "free-model"}}
	_, err := buildTarget(decision, "", cfg, redact.New(), nil)
	if err == nil {
		t.Fatal("want unconfirmed-endpoint error")
	}
	for _, want := range []string{"endpoint unconfirmed", "providers.free-cloud.base_url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q lacks %q", err, want)
		}
	}
}

// TestBuildTargetKeylessLocalStaysQuiet: keyless local providers
// (ollama) are the normal case — no warning.
func TestBuildTargetKeylessLocalStaysQuiet(t *testing.T) {
	decision := core.RouteDecision{Selected: core.ModelRoute{Provider: "ollama", Model: "llama-test"}}
	target, err := buildTarget(decision, "", config.Config{}, redact.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer target.creds.close()
	if warned := target.warns.drain(); len(warned) != 0 {
		t.Errorf("warnings = %q, want none for a keyless local provider", warned)
	}
}

func TestProviderIsKeyless(t *testing.T) {
	entry := func(id string) providers.Entry {
		e, ok := providers.Lookup(id)
		if !ok {
			t.Fatalf("registry missing %q", id)
		}
		return e
	}
	none := func(string) (string, bool) { return "", false }
	withAuth := config.ProviderConfig{Auth: &config.AuthRef{Source: "env", Name: "K"}}

	cases := []struct {
		name       string
		isRegistry bool
		entry      providers.Entry
		pc         config.ProviderConfig
		lookup     func(string) (string, bool)
		want       bool
	}{
		{"custom no auth", false, providers.Entry{}, config.ProviderConfig{}, none, true},
		{"custom with auth", false, providers.Entry{}, withAuth, none, false},
		{"registry auth-none", true, entry("ollama"), config.ProviderConfig{}, none, true},
		{"registry auth-none but user configured auth", true, entry("ollama"), withAuth, none, false},
		{"registry optional, no env", true, entry("actual"), config.ProviderConfig{}, none, true},
		{"registry optional, env set", true, entry("actual"), config.ProviderConfig{}, func(k string) (string, bool) {
			if k == "ACTUAL_API_KEY" {
				return "sk-live", true
			}
			return "", false
		}, false},
		{"registry required key, no env", true, entry("fireworks"), config.ProviderConfig{}, none, false},
	}
	for _, tc := range cases {
		if got := providerIsKeyless(tc.isRegistry, tc.entry, tc.pc, tc.lookup); got != tc.want {
			t.Errorf("%s: providerIsKeyless = %v, want %v", tc.name, got, tc.want)
		}
	}
}
