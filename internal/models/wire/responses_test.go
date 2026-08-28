package wire_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/models/wire"
)

func newResponses(t *testing.T, srv *httptest.Server, mod func(*wire.Options)) *wire.Responses {
	t.Helper()
	o := wire.Options{
		ID:         "resprov",
		BaseURL:    srv.URL + "/v1",
		Model:      "test-model",
		Privacy:    core.PrivacyPublicCloud,
		Credential: credFunc(t),
		HTTPClient: srv.Client(),
	}
	if mod != nil {
		mod(&o)
	}
	p, err := wire.NewResponses(o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResponsesPlainText(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{
			"status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello there"}]}],
			"usage":{"input_tokens":10,"output_tokens":4,"input_tokens_details":{"cached_tokens":3}}
		}`))
	}))
	defer srv.Close()
	p := newResponses(t, srv, nil)
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
		t.Fatalf("auth = %q", gotAuth)
	}
	input := gotBody["input"].([]any)
	if len(input) != 2 || input[0].(map[string]any)["role"] != "system" {
		t.Fatalf("input = %v", input)
	}
}

func TestResponsesToolCallMapping(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{
			"status":"completed",
			"output":[{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a.go\"}"}],
			"usage":{"input_tokens":5,"output_tokens":2}
		}`))
	}))
	defer srv.Close()
	p := newResponses(t, srv, nil)
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
	if tools[0].(map[string]any)["type"] != "function" || tools[0].(map[string]any)["name"] != "read_file" {
		t.Fatalf("tools = %v", tools)
	}
	input := gotBody["input"].([]any)
	fc := input[1].(map[string]any)
	if fc["type"] != "function_call" || fc["call_id"] != "call_0" {
		t.Fatalf("function_call item = %v", fc)
	}
	fo := input[2].(map[string]any)
	if fo["type"] != "function_call_output" || fo["call_id"] != "call_0" || fo["output"] != "a.go" {
		t.Fatalf("function_call_output item = %v", fo)
	}
}

func TestResponsesStructuredOutput(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"x\":1}"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	p := newResponses(t, srv, nil)
	if _, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:        "test-model",
		Messages:     []core.Message{{Role: core.RoleUser, Content: "x"}},
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}); err != nil {
		t.Fatal(err)
	}
	text, ok := gotBody["text"].(map[string]any)
	if !ok {
		t.Fatalf("text config missing: %v", gotBody)
	}
	format := text["format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Fatalf("format = %v", format)
	}
}

func respSSE(events ...[2]string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("event: " + e[0] + "\ndata: " + e[1] + "\n\n")
	}
	return b.String()
}

func TestResponsesStreamedText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body["stream"] != true {
			t.Error("stream not requested")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, respSSE(
			[2]string{"response.output_text.delta", `{"delta":"hel"}`},
			[2]string{"response.output_text.delta", `{"delta":"lo"}`},
			[2]string{"response.completed", `{"response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":7,"output_tokens":2}}}`},
		))
	}))
	defer srv.Close()
	var deltas []string
	p := newResponses(t, srv, func(o *wire.Options) {
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

func TestResponsesStreamedToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, respSSE(
			[2]string{"response.completed", `{"response":{"status":"completed","output":[{"type":"function_call","call_id":"call_9","name":"grep","arguments":"{\"q\":\"x\"}"}],"usage":{"input_tokens":3,"output_tokens":1}}}`},
		))
	}))
	defer srv.Close()
	p := newResponses(t, srv, func(o *wire.Options) { o.OnDelta = func(string) {} })
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
		t.Fatalf("tool call = %+v args=%s", tc, tc.Arguments)
	}
}

func TestResponsesErrorsTypedAndClean(t *testing.T) {
	for _, status := range []int{401, 429} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"message":"denied `+testSecret+`"}}`)
		}))
		p := newResponses(t, srv, nil)
		_, err := p.Complete(context.Background(), core.CompletionRequest{
			Model:    "test-model",
			Messages: []core.Message{{Role: core.RoleUser, Content: "x"}},
		})
		srv.Close()
		var we *wire.Error
		if !errors.As(err, &we) || we.Status != status || we.Provider != "resprov" {
			t.Fatalf("status %d: err = %v", status, err)
		}
		if strings.Contains(err.Error(), testSecret) {
			t.Fatalf("credential leaked: %v", err)
		}
	}
}

func TestResponsesMalformedSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {not json}\n\n")
	}))
	defer srv.Close()
	p := newResponses(t, srv, func(o *wire.Options) { o.OnDelta = func(string) {} })
	if _, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:    "test-model",
		Messages: []core.Message{{Role: core.RoleUser, Content: "x"}},
	}); err == nil {
		t.Fatal("malformed SSE must fail")
	}
}

func TestResponsesDescriptor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	d := newResponses(t, srv, nil).Descriptor()
	if d.ID != "resprov" || d.Kind != core.ProviderResponses {
		t.Fatalf("descriptor = %+v", d)
	}
}
