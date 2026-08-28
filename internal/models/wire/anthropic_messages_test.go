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

func newAnthropic(t *testing.T, srv *httptest.Server, mod func(*wire.Options)) *wire.AnthropicMessages {
	t.Helper()
	o := wire.Options{
		ID:         "anthprov",
		BaseURL:    srv.URL + "/anthropic",
		Model:      "test-model",
		Privacy:    core.PrivacyPublicCloud,
		Credential: credFunc(t),
		HTTPClient: srv.Client(),
	}
	if mod != nil {
		mod(&o)
	}
	p, err := wire.NewAnthropicMessages(o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAnthropicPlainText(t *testing.T) {
	var gotBody map[string]any
	var gotKey, gotVersion, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anthropic/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{
			"content":[{"type":"text","text":"hello there"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":3}
		}`))
	}))
	defer srv.Close()
	p := newAnthropic(t, srv, nil)
	resp, err := p.Complete(context.Background(), core.CompletionRequest{
		Model: "test-model",
		Messages: []core.Message{
			{Role: core.RoleSystem, Content: "be brief"},
			{Role: core.RoleUser, Content: "hi"},
		},
		MaxOutputTokens: 512,
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
	if gotKey != testSecret {
		t.Fatalf("x-api-key = %q", gotKey)
	}
	if gotVersion == "" {
		t.Fatal("anthropic-version header missing")
	}
	if gotAuth != "" {
		t.Fatalf("anthropic wire must not send Authorization, got %q", gotAuth)
	}
	if gotBody["system"] != "be brief" {
		t.Fatalf("system = %v", gotBody["system"])
	}
	if gotBody["max_tokens"] != float64(512) {
		t.Fatalf("max_tokens = %v", gotBody["max_tokens"])
	}
	msgs := gotBody["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages = %v", msgs)
	}
}

func TestAnthropicToolCallMapping(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{
			"content":[{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"a.go"}}],
			"stop_reason":"tool_use",
			"usage":{"input_tokens":5,"output_tokens":2}
		}`))
	}))
	defer srv.Close()
	p := newAnthropic(t, srv, nil)
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
	if tools[0].(map[string]any)["name"] != "read_file" || tools[0].(map[string]any)["input_schema"] == nil {
		t.Fatalf("tools = %v", tools)
	}
	msgs := gotBody["messages"].([]any)
	asstContent := msgs[1].(map[string]any)["content"].([]any)
	var sawToolUse bool
	for _, block := range asstContent {
		if block.(map[string]any)["type"] == "tool_use" {
			sawToolUse = true
			if block.(map[string]any)["id"] != "call_0" {
				t.Fatalf("tool_use id = %v", block)
			}
		}
	}
	if !sawToolUse {
		t.Fatalf("assistant content lacks tool_use: %v", asstContent)
	}
	resultMsg := msgs[2].(map[string]any)
	if resultMsg["role"] != "user" {
		t.Fatalf("tool_result must ride a user message, got %v", resultMsg["role"])
	}
	resultBlock := resultMsg["content"].([]any)[0].(map[string]any)
	if resultBlock["type"] != "tool_result" || resultBlock["tool_use_id"] != "call_0" {
		t.Fatalf("tool_result block = %v", resultBlock)
	}
}

func TestAnthropicStructuredOutputNotImplemented(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("request must not be sent")
	}))
	defer srv.Close()
	p := newAnthropic(t, srv, nil)
	_, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:        "test-model",
		Messages:     []core.Message{{Role: core.RoleUser, Content: "x"}},
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	})
	if !errors.Is(err, core.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
}

func TestAnthropicStreamedText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body["stream"] != true {
			t.Error("stream not requested")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, respSSE(
			[2]string{"message_start", `{"message":{"usage":{"input_tokens":7,"output_tokens":0}}}`},
			[2]string{"content_block_start", `{"index":0,"content_block":{"type":"text"}}`},
			[2]string{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"hel"}}`},
			[2]string{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"lo"}}`},
			[2]string{"content_block_stop", `{"index":0}`},
			[2]string{"message_delta", `{"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`},
			[2]string{"message_stop", `{}`},
		))
	}))
	defer srv.Close()
	var deltas []string
	p := newAnthropic(t, srv, func(o *wire.Options) {
		o.OnDelta = func(d string) { deltas = append(deltas, d) }
	})
	resp, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:           "test-model",
		Messages:        []core.Message{{Role: core.RoleUser, Content: "hi"}},
		MaxOutputTokens: 100,
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

func TestAnthropicStreamedToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, respSSE(
			[2]string{"message_start", `{"message":{"usage":{"input_tokens":3,"output_tokens":0}}}`},
			[2]string{"content_block_start", `{"index":0,"content_block":{"type":"tool_use","id":"call_9","name":"grep"}}`},
			[2]string{"content_block_delta", `{"index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`},
			[2]string{"content_block_delta", `{"index":0,"delta":{"type":"input_json_delta","partial_json":"\"x\"}"}}`},
			[2]string{"content_block_stop", `{"index":0}`},
			[2]string{"message_delta", `{"delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`},
			[2]string{"message_stop", `{}`},
		))
	}))
	defer srv.Close()
	p := newAnthropic(t, srv, func(o *wire.Options) { o.OnDelta = func(string) {} })
	resp, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:           "test-model",
		Messages:        []core.Message{{Role: core.RoleUser, Content: "grep x"}},
		MaxOutputTokens: 100,
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

func TestAnthropicErrorsTypedAndClean(t *testing.T) {
	for _, status := range []int{401, 429} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"message":"denied `+testSecret+`"}}`)
		}))
		p := newAnthropic(t, srv, nil)
		_, err := p.Complete(context.Background(), core.CompletionRequest{
			Model:           "test-model",
			Messages:        []core.Message{{Role: core.RoleUser, Content: "x"}},
			MaxOutputTokens: 10,
		})
		srv.Close()
		var we *wire.Error
		if !errors.As(err, &we) || we.Status != status || we.Provider != "anthprov" {
			t.Fatalf("status %d: err = %v", status, err)
		}
		if strings.Contains(err.Error(), testSecret) {
			t.Fatalf("credential leaked: %v", err)
		}
	}
}

func TestAnthropicMalformedSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {not json}\n\n")
	}))
	defer srv.Close()
	p := newAnthropic(t, srv, func(o *wire.Options) { o.OnDelta = func(string) {} })
	if _, err := p.Complete(context.Background(), core.CompletionRequest{
		Model:           "test-model",
		Messages:        []core.Message{{Role: core.RoleUser, Content: "x"}},
		MaxOutputTokens: 10,
	}); err == nil {
		t.Fatal("malformed SSE must fail")
	}
}

func TestAnthropicDescriptor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	d := newAnthropic(t, srv, nil).Descriptor()
	if d.ID != "anthprov" || d.Kind != core.ProviderAnthropic {
		t.Fatalf("descriptor = %+v", d)
	}
	if d.Capabilities.StructuredOutput {
		t.Fatal("anthropic wire must not advertise structured output")
	}
}
