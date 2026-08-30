package wire_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ataidesorg/ink/internal/auth"
	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/models/wire"
)

var testSecret = strings.Join([]string{"tok", "wire", "secret", "1234567890"}, "-") // fragments for secret scanners

// fakeRegistrar satisfies auth.Registrar without pulling in redact.
type fakeRegistrar struct{ saw []string }

func (f *fakeRegistrar) AddLiteral(literals ...string) { f.saw = append(f.saw, literals...) }

func credFunc(t *testing.T) func(context.Context) (*auth.Credential, error) {
	t.Helper()
	reg := &fakeRegistrar{}
	cred := auth.NewCredential(reg, testSecret)
	if len(reg.saw) != 1 || reg.saw[0] != testSecret {
		t.Fatal("NewCredential must register the literal before returning")
	}
	return func(_ context.Context) (*auth.Credential, error) { return cred, nil }
}

func newProvider(t *testing.T, srv *httptest.Server, mod func(*wire.Options)) *wire.ChatCompletions {
	t.Helper()
	o := wire.Options{
		ID:         "testprov",
		BaseURL:    srv.URL + "/v1",
		Model:      "test-model",
		Privacy:    core.PrivacyPublicCloud,
		Credential: credFunc(t),
		HTTPClient: srv.Client(),
	}
	if mod != nil {
		mod(&o)
	}
	p, err := wire.NewChatCompletions(o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlainTextRoundTrip(t *testing.T) {
	var gotBody map[string]any
	var gotAuth, gotExtra string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotExtra = r.Header.Get("X-Extra")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"hello there"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":3}}
		}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv, func(o *wire.Options) { o.Headers = map[string]string{"X-Extra": "yes"} })
	resp, err := p.Complete(context.Background(), core.CompletionRequest{
		Model: "test-model",
		Messages: []core.Message{
			{Role: core.RoleSystem, Content: "be brief"},
			{Role: core.RoleUser, Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Content != "hello there" || resp.Finish != core.FinishStop {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 4 || resp.Usage.CachedInputTokens != 3 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if gotAuth != "Bearer "+testSecret {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotExtra != "yes" {
		t.Fatal("per-entry header override missing")
	}
	if gotBody["model"] != "test-model" {
		t.Fatalf("body model = %v", gotBody["model"])
	}
	msgs := gotBody["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages = %v", msgs)
	}
}

func TestToolCallMapping(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}
			]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":5,"completion_tokens":2}
		}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv, nil)
	resp, err := p.Complete(context.Background(), core.CompletionRequest{
		Model: "test-model",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "read a.go"},
			{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{ID: "call_0", Name: "ls", Arguments: json.RawMessage(`{}`)}}},
			{Role: core.RoleTool, ToolCallID: "call_0", Name: "ls", Content: "a.go"},
		},
		Tools: []core.ToolSpec{{Name: "read_file", Description: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Finish != core.FinishToolCalls || len(resp.ToolCalls) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "read_file" || !strings.Contains(string(tc.Arguments), "a.go") {
		t.Fatalf("tool call = %+v", tc)
	}
	tools := gotBody["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if tools[0].(map[string]any)["type"] != "function" || fn["name"] != "read_file" {
		t.Fatalf("tools = %v", tools)
	}
	msgs := gotBody["messages"].([]any)
	asst := msgs[1].(map[string]any)
	if asst["tool_calls"] == nil {
		t.Fatal("assistant tool_calls not mapped")
	}
	toolMsg := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_0" {
		t.Fatalf("tool result msg = %v", toolMsg)
	}
}

func TestStructuredOutputRequest(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"x\":1}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv, nil)
	_, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:        "test-model",
		Messages:     []core.Message{{Role: core.RoleUser, Content: "x"}},
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"number"}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	rf, ok := gotBody["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_schema" {
		t.Fatalf("response_format = %v", gotBody["response_format"])
	}
}

func sseBody(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("data: " + l + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func TestStreamedText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body["stream"] != true {
			t.Error("stream not requested despite observer")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseBody(
			`{"choices":[{"delta":{"role":"assistant","content":"hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`,
		))
	}))
	defer srv.Close()
	var deltas []string
	p := newProvider(t, srv, func(o *wire.Options) {
		o.OnDelta = func(d string) { deltas = append(deltas, d) }
	})
	resp, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:    "test-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Content != "hello" || resp.Finish != core.FinishStop {
		t.Fatalf("resp = %+v", resp)
	}
	if strings.Join(deltas, "|") != "hel|lo" {
		t.Fatalf("deltas = %v", deltas)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestStreamedToolCallAccumulation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseBody(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"grep","arguments":"{\"q\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
		))
	}))
	defer srv.Close()
	p := newProvider(t, srv, func(o *wire.Options) { o.OnDelta = func(string) {} })
	resp, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:    "test-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "grep x"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Finish != core.FinishToolCalls || len(resp.ToolCalls) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_9" || tc.Name != "grep" || string(tc.Arguments) != `{"q":"x"}` {
		t.Fatalf("tool call = %+v, args=%s", tc, tc.Arguments)
	}
}

func TestAuthAndRateErrorsTypedAndClean(t *testing.T) {
	for _, tc := range []struct{ status int }{{401}, {429}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, `{"error":{"message":"denied `+testSecret+`"}}`)
		}))
		p := newProvider(t, srv, nil)
		_, err := p.Complete(context.Background(), core.CompletionRequest{
			Model:    "test-model",
			Messages: []core.Message{{Role: core.RoleUser, Content: "x"}},
		})
		srv.Close()
		var we *wire.Error
		if !errors.As(err, &we) {
			t.Fatalf("status %d: want *wire.Error, got %T: %v", tc.status, err, err)
		}
		if we.Status != tc.status || we.Provider != "testprov" {
			t.Fatalf("wire error = %+v", we)
		}
		if strings.Contains(err.Error(), testSecret) {
			t.Fatalf("credential leaked into error: %v", err)
		}
	}
}

func TestMalformedSSEFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {not json}\n\n")
	}))
	defer srv.Close()
	p := newProvider(t, srv, func(o *wire.Options) { o.OnDelta = func(string) {} })
	_, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:    "test-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "x"}},
	})
	if err == nil {
		t.Fatal("malformed SSE must fail, not return partial silently")
	}
}

func TestServerErrorRetriesOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv, nil)
	resp, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:    "test-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "x"}},
	})
	if err != nil {
		t.Fatalf("complete after retry: %v", err)
	}
	if resp.Content != "ok" || calls.Load() != 2 {
		t.Fatalf("content=%q calls=%d", resp.Content, calls.Load())
	}
}

func TestKeylessSendsNoAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	p := newProvider(t, srv, func(o *wire.Options) { o.Credential = nil })
	if _, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:    "test-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "x"}},
	}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Fatalf("keyless provider sent Authorization %q", gotAuth)
	}
}

func TestDescriptor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	p := newProvider(t, srv, nil)
	d := p.Descriptor()
	if d.ID != "testprov" || d.Kind != core.ProviderOpenAICompatible || d.Privacy != core.PrivacyPublicCloud {
		t.Fatalf("descriptor = %+v", d)
	}
	if !d.Capabilities.Streaming || !d.Capabilities.ToolCalling {
		t.Fatalf("capabilities = %+v", d.Capabilities)
	}
}

func TestNewChatCompletionsValidates(t *testing.T) {
	if _, err := wire.NewChatCompletions(wire.Options{ID: "x", Model: "m"}); err == nil {
		t.Fatal("missing base URL must fail")
	}
	if _, err := wire.NewChatCompletions(wire.Options{BaseURL: "http://x", Model: "m"}); err == nil {
		t.Fatal("missing id must fail")
	}
}
