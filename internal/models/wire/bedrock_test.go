package wire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ataidesorg/friday/internal/core"
)

func fakeSigner(t *testing.T) func(ctx context.Context, req *http.Request, payload []byte) error {
	t.Helper()
	return func(_ context.Context, req *http.Request, payload []byte) error {
		req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 test len=%d", len(payload)))
		req.Header.Set("X-Amz-Date", "20260824T120000Z")
		return nil
	}
}

func TestBedrockConverseRoundTrip(t *testing.T) {
	var gotPath, gotAuth, gotDate string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotAuth = r.Header.Get("Authorization")
		gotDate = r.Header.Get("X-Amz-Date")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `{
			"output":{"message":{"role":"assistant","content":[
				{"text":"Reading the file."},
				{"toolUse":{"toolUseId":"tu-1","name":"read_file","input":{"path":"greet.go"}}}
			]}},
			"stopReason":"tool_use",
			"usage":{"inputTokens":420,"outputTokens":32}
		}`)
	}))
	defer srv.Close()

	var deltas []string
	p, err := NewBedrock(Options{
		ID: "bedrock", BaseURL: srv.URL, Model: "anthropic.claude-3-5-sonnet-20240620-v1:0",
		Sign:    fakeSigner(t),
		OnDelta: func(d string) { deltas = append(deltas, d) },
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), core.CompletionRequest{
		Model: "ignored-by-wire",
		Messages: []core.Message{
			{Role: core.RoleSystem, Content: "Be terse."},
			{Role: core.RoleUser, Content: "read greet.go"},
			{Role: core.RoleAssistant, Content: "ok", ToolCalls: []core.ToolCall{
				{ID: "tu-0", Name: "read_file", Arguments: json.RawMessage(`{"path":"a.go"}`)},
			}},
			{Role: core.RoleTool, ToolCallID: "tu-0", Content: "package a"},
			{Role: core.RoleTool, ToolCallID: "tu-0b", Content: "second result"},
			{Role: core.RoleUser, Content: "now greet.go"},
		},
		Tools: []core.ToolSpec{{
			Name: "read_file", Description: "read a file",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
		MaxOutputTokens: 512,
	})
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/model/anthropic.claude-3-5-sonnet-20240620-v1:0/converse" &&
		gotPath != "/model/anthropic.claude-3-5-sonnet-20240620-v1%3A0/converse" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256") || gotDate == "" {
		t.Errorf("signer did not run: auth=%q date=%q", gotAuth, gotDate)
	}
	sys := gotBody["system"].([]any)
	if sys[0].(map[string]any)["text"] != "Be terse." {
		t.Errorf("system = %v", sys)
	}
	msgs := gotBody["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4 (tool results folded into one user turn)", len(msgs))
	}
	toolTurn := msgs[2].(map[string]any)
	if toolTurn["role"] != "user" || len(toolTurn["content"].([]any)) != 2 {
		t.Errorf("tool-result turn = %v", toolTurn)
	}
	tr := toolTurn["content"].([]any)[0].(map[string]any)["toolResult"].(map[string]any)
	if tr["toolUseId"] != "tu-0" {
		t.Errorf("toolResult = %v", tr)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	tu := asst[1].(map[string]any)["toolUse"].(map[string]any)
	if tu["toolUseId"] != "tu-0" || tu["name"] != "read_file" {
		t.Errorf("toolUse = %v", tu)
	}
	inf := gotBody["inferenceConfig"].(map[string]any)
	if inf["maxTokens"] != float64(512) {
		t.Errorf("inferenceConfig = %v", inf)
	}
	spec := gotBody["toolConfig"].(map[string]any)["tools"].([]any)[0].(map[string]any)["toolSpec"].(map[string]any)
	if spec["name"] != "read_file" || spec["inputSchema"].(map[string]any)["json"] == nil {
		t.Errorf("toolSpec = %v", spec)
	}

	if resp.Content != "Reading the file." || resp.Finish != core.FinishToolCalls {
		t.Errorf("resp = %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "tu-1" || resp.ToolCalls[0].Name != "read_file" {
		t.Errorf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.Usage.InputTokens != 420 || resp.Usage.OutputTokens != 32 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if len(deltas) != 1 || deltas[0] != "Reading the file." {
		t.Errorf("deltas = %q, want one final delta", deltas)
	}
}

func TestBedrockFinishMapping(t *testing.T) {
	cases := map[string]core.FinishReason{
		"end_turn":      core.FinishStop,
		"stop_sequence": core.FinishStop,
		"max_tokens":    core.FinishLength,
		"tool_use":      core.FinishToolCalls,
	}
	for stop, want := range cases {
		if got := bedrockFinish(stop, false); got != want {
			t.Errorf("bedrockFinish(%q) = %q, want %q", stop, got, want)
		}
	}
	if got := bedrockFinish("unknown", true); got != core.FinishToolCalls {
		t.Errorf("unknown+toolUse = %q", got)
	}
}

func TestBedrockRequiresSigner(t *testing.T) {
	_, err := NewBedrock(Options{ID: "bedrock", BaseURL: "https://x", Model: "m"})
	if err == nil || !errors.Is(err, core.ErrInvalidInput) || !strings.Contains(err.Error(), "signer") {
		t.Fatalf("err = %v", err)
	}
}

func TestBedrockStructuredOutputNotImplemented(t *testing.T) {
	p, err := NewBedrock(Options{ID: "bedrock", BaseURL: "https://x", Model: "m", Sign: fakeSigner(t)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), core.CompletionRequest{
		Messages:     []core.Message{{Role: core.RoleUser, Content: "hi"}},
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	})
	if !errors.Is(err, core.ErrNotImplemented) {
		t.Fatalf("err = %v, want NotImplemented", err)
	}
}

func TestBedrockStatusErrorNeverLeaksBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"secret-echo-1 invalid signature"}`)
	}))
	defer srv.Close()
	p, err := NewBedrock(Options{ID: "bedrock", BaseURL: srv.URL, Model: "m", Sign: fakeSigner(t)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), core.CompletionRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	var we *Error
	if !errors.As(err, &we) || we.Status != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-echo-1") {
		t.Error("error leaks response body")
	}
}

func TestBedrockDescriptor(t *testing.T) {
	p, err := NewBedrock(Options{ID: "bedrock", BaseURL: "https://x", Model: "m", Sign: fakeSigner(t)})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Descriptor()
	if d.Kind != core.ProviderBedrock || d.Capabilities.Streaming || !d.Capabilities.ToolCalling || d.Capabilities.StructuredOutput {
		t.Errorf("descriptor = %+v", d)
	}
}
