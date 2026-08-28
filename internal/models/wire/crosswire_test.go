package wire_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ataidesorg/friday/internal/core"
)

// TestCrossWireEquivalence sends one request with a completed tool
// round-trip through all three wires; each dialect's fixture encodes the
// same semantic answer and every wire must map it to the same core response.
func TestCrossWireEquivalence(t *testing.T) {
	req := core.CompletionRequest{
		Model: "test-model",
		Messages: []core.Message{
			{Role: core.RoleSystem, Content: "be brief"},
			{Role: core.RoleUser, Content: "read a.go"},
			{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{ID: "call_0", Name: "read_file", Arguments: json.RawMessage(`{"path":"a.go"}`)}}},
			{Role: core.RoleTool, ToolCallID: "call_0", Name: "read_file", Content: "package main"},
		},
		Tools:           []core.ToolSpec{{Name: "read_file", Description: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		MaxOutputTokens: 100,
	}
	want := core.CompletionResponse{
		Content: "a.go holds package main",
		Usage:   core.Usage{InputTokens: 20, OutputTokens: 6, CachedInputTokens: 2},
		Finish:  core.FinishStop,
	}
	build := map[string]func(t *testing.T) core.ModelProvider{
		"chat_completions": func(t *testing.T) core.ModelProvider {
			srv := serveJSON(t, `{
				"choices":[{"message":{"content":"a.go holds package main"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":20,"completion_tokens":6,"prompt_tokens_details":{"cached_tokens":2}}
			}`)
			return newProvider(t, srv, nil)
		},
		"responses": func(t *testing.T) core.ModelProvider {
			srv := serveJSON(t, `{
				"status":"completed",
				"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a.go holds package main"}]}],
				"usage":{"input_tokens":20,"output_tokens":6,"input_tokens_details":{"cached_tokens":2}}
			}`)
			return newResponses(t, srv, nil)
		},
		"anthropic_messages": func(t *testing.T) core.ModelProvider {
			srv := serveJSON(t, `{
				"content":[{"type":"text","text":"a.go holds package main"}],
				"stop_reason":"end_turn",
				"usage":{"input_tokens":20,"output_tokens":6,"cache_read_input_tokens":2}
			}`)
			return newAnthropic(t, srv, nil)
		},
	}
	for name, mk := range build {
		t.Run(name, func(t *testing.T) {
			got, err := mk(t).Complete(context.Background(), req)
			if err != nil {
				t.Fatalf("complete: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("wire %s diverges:\ngot  %+v\nwant %+v", name, got, want)
			}
		})
	}
}

func serveJSON(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}
