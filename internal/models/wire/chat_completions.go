package wire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/ataidesorg/ink/internal/core"
)

// ChatCompletions speaks the OpenAI-compatible POST /chat/completions
// dialect, the wire shared by most registry providers.
type ChatCompletions struct {
	opts     Options
	client   *http.Client
	endpoint string
}

// NewChatCompletions validates the options and returns the provider.
func NewChatCompletions(o Options) (*ChatCompletions, error) {
	if err := validateOptions(o); err != nil {
		return nil, err
	}
	return &ChatCompletions{
		opts:     o,
		client:   clientFor(o),
		endpoint: strings.TrimRight(o.BaseURL, "/") + "/chat/completions",
	}, nil
}

// Descriptor reports identity and capabilities; health stays unverified
// until a real call succeeds (probes update it).
func (p *ChatCompletions) Descriptor() core.ProviderDescriptor {
	return core.ProviderDescriptor{
		ID:      p.opts.ID,
		Kind:    core.ProviderOpenAICompatible,
		Privacy: p.opts.Privacy,
		Capabilities: core.ProviderCapabilities{
			Streaming:        true,
			ToolCalling:      true,
			StructuredOutput: true,
			MaxContextTokens: p.opts.MaxContextTokens,
		},
		Health: core.ProviderHealth{State: core.HealthUnknown},
	}
}

// Complete sends the request; with OnDelta set it streams and accumulates.
func (p *ChatCompletions) Complete(ctx context.Context, req core.CompletionRequest) (core.CompletionResponse, error) {
	stream := p.opts.OnDelta != nil
	payload, err := json.Marshal(buildCCRequest(req, p.opts.Model, stream))
	if err != nil {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: encode request: %w", p.opts.ID, err)
	}
	resp, err := postAuthed(ctx, p.opts, p.client, p.endpoint, payload, func(h http.Header, authValue string) {
		if authValue != "" {
			h.Set("Authorization", "Bearer "+authValue)
		}
		if stream {
			h.Set("Accept", "text/event-stream")
		}
		for k, v := range p.opts.Headers {
			h.Set(k, v)
		}
	})
	if err != nil {
		return core.CompletionResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if stream {
		return p.readStream(resp)
	}
	return p.readOnce(resp)
}

func (p *ChatCompletions) readOnce(resp *http.Response) (core.CompletionResponse, error) {
	var body ccResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: decode response: %w", p.opts.ID, err)
	}
	if len(body.Choices) == 0 {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: response held no choices", p.opts.ID)
	}
	choice := body.Choices[0]
	out := core.CompletionResponse{
		Content:   choice.Message.Content,
		ToolCalls: toCoreToolCalls(choice.Message.ToolCalls),
		Usage:     toCoreUsage(body.Usage),
		Finish:    toFinish(choice.FinishReason, len(choice.Message.ToolCalls) > 0),
	}
	return out, nil
}

func (p *ChatCompletions) readStream(resp *http.Response) (core.CompletionResponse, error) {
	var (
		content strings.Builder
		usage   core.Usage
		finish  = core.FinishStop
		calls   = map[int]*ccStreamCall{}
	)
	err := readSSEEvents(resp.Body, func(_, data string) (bool, error) {
		if data == "[DONE]" {
			return true, nil
		}
		var chunk ccChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return false, fmt.Errorf("malformed SSE payload: %w", err)
		}
		if chunk.Usage != nil {
			usage = toCoreUsage(chunk.Usage)
		}
		if len(chunk.Choices) == 0 {
			return false, nil
		}
		choice := chunk.Choices[0]
		if choice.Delta.Content != "" {
			content.WriteString(choice.Delta.Content)
			p.opts.OnDelta(choice.Delta.Content)
		}
		for _, tc := range choice.Delta.ToolCalls {
			call := calls[tc.Index]
			if call == nil {
				call = &ccStreamCall{index: tc.Index}
				calls[tc.Index] = call
			}
			if tc.ID != "" {
				call.id = tc.ID
			}
			if tc.Function.Name != "" {
				call.name = tc.Function.Name
			}
			call.args.WriteString(tc.Function.Arguments)
		}
		if choice.FinishReason != "" {
			finish = toFinish(choice.FinishReason, len(calls) > 0)
		}
		return false, nil
	})
	if err != nil {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: read stream: %w", p.opts.ID, err)
	}
	return core.CompletionResponse{
		Content:   content.String(),
		ToolCalls: streamCallsToCore(calls),
		Usage:     usage,
		Finish:    finish,
	}, nil
}

// ccStreamCall accumulates one tool call across SSE chunks by index.
type ccStreamCall struct {
	index int
	id    string
	name  string
	args  strings.Builder
}

func streamCallsToCore(calls map[int]*ccStreamCall) []core.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	ordered := make([]*ccStreamCall, 0, len(calls))
	for _, c := range calls {
		ordered = append(ordered, c)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].index < ordered[j].index })
	out := make([]core.ToolCall, 0, len(ordered))
	for _, c := range ordered {
		out = append(out, core.ToolCall{
			ID:        core.ToolCallID(c.id),
			Name:      c.name,
			Arguments: json.RawMessage(c.args.String()),
		})
	}
	return out
}

// --- request/response body shapes (OpenAI chat completions dialect) ---

type ccRequest struct {
	Model          string            `json:"model"`
	Messages       []ccMessage       `json:"messages"`
	Tools          []ccTool          `json:"tools,omitempty"`
	MaxTokens      int               `json:"max_tokens,omitempty"`
	ResponseFormat *ccResponseFormat `json:"response_format,omitempty"`
	Stream         bool              `json:"stream,omitempty"`
	StreamOptions  *ccStreamOptions  `json:"stream_options,omitempty"`
}

type ccMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	Images     []core.ImagePart `json:"-"` // rendered by MarshalJSON as a content-part array
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
	ToolCalls  []ccToolCall     `json:"tool_calls,omitempty"`
}

// MarshalJSON keeps plain-text messages as a string content field and
// switches to the content-part array form only when images ride along.
// Request-only: responses decode into ccRespMessage.
func (m ccMessage) MarshalJSON() ([]byte, error) {
	type plain ccMessage
	if len(m.Images) == 0 {
		return json.Marshal(plain(m))
	}
	parts := make([]map[string]any, 0, 1+len(m.Images))
	if m.Content != "" {
		parts = append(parts, map[string]any{"type": "text", "text": m.Content})
	}
	for _, img := range m.Images {
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": "data:" + img.MediaType + ";base64," + img.Data},
		})
	}
	shadow := plain(m)
	shadow.Content = ""
	raw, err := json.Marshal(shadow)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if obj["content"], err = json.Marshal(parts); err != nil {
		return nil, err
	}
	return json.Marshal(obj)
}

type ccToolCall struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Function ccFunction `json:"function"`
}

type ccFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ccTool struct {
	Type     string    `json:"type"`
	Function ccToolDef `json:"function"`
}

type ccToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ccResponseFormat struct {
	Type       string       `json:"type"`
	JSONSchema ccJSONSchema `json:"json_schema"`
}

type ccJSONSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type ccStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ccResponse struct {
	Choices []ccChoice `json:"choices"`
	Usage   *ccUsage   `json:"usage"`
}

type ccChoice struct {
	Message      ccRespMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type ccRespMessage struct {
	Content   string       `json:"content"`
	ToolCalls []ccToolCall `json:"tool_calls"`
}

type ccUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type ccChunk struct {
	Choices []ccChunkChoice `json:"choices"`
	Usage   *ccUsage        `json:"usage"`
}

type ccChunkChoice struct {
	Delta        ccDelta `json:"delta"`
	FinishReason string  `json:"finish_reason"`
}

type ccDelta struct {
	Content   string            `json:"content"`
	ToolCalls []ccDeltaToolCall `json:"tool_calls"`
}

type ccDeltaToolCall struct {
	Index    int        `json:"index"`
	ID       string     `json:"id"`
	Function ccFunction `json:"function"`
}

// --- mapping ---

func buildCCRequest(req core.CompletionRequest, model string, stream bool) ccRequest {
	out := ccRequest{
		Model:     model,
		Messages:  toCCMessages(req.Messages),
		Tools:     toCCTools(req.Tools),
		MaxTokens: req.MaxOutputTokens,
		Stream:    stream,
	}
	if stream {
		out.StreamOptions = &ccStreamOptions{IncludeUsage: true}
	}
	if len(req.OutputSchema) > 0 {
		out.ResponseFormat = &ccResponseFormat{
			Type:       "json_schema",
			JSONSchema: ccJSONSchema{Name: "ink_output", Schema: req.OutputSchema, Strict: true},
		}
	}
	return out
}

func toCCMessages(msgs []core.Message) []ccMessage {
	out := make([]ccMessage, 0, len(msgs))
	for _, m := range msgs {
		cm := ccMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			Images:     m.Images,
			ToolCallID: string(m.ToolCallID),
			Name:       m.Name,
		}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, ccToolCall{
				ID:       string(tc.ID),
				Type:     "function",
				Function: ccFunction{Name: tc.Name, Arguments: string(tc.Arguments)},
			})
		}
		out = append(out, cm)
	}
	return out
}

func toCCTools(tools []core.ToolSpec) []ccTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]ccTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, ccTool{
			Type:     "function",
			Function: ccToolDef{Name: t.Name, Description: t.Description, Parameters: t.InputSchema},
		})
	}
	return out
}

func toCoreToolCalls(calls []ccToolCall) []core.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]core.ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, core.ToolCall{
			ID:        core.ToolCallID(c.ID),
			Name:      c.Function.Name,
			Arguments: json.RawMessage(c.Function.Arguments),
		})
	}
	return out
}

func toCoreUsage(u *ccUsage) core.Usage {
	if u == nil {
		return core.Usage{}
	}
	return core.Usage{
		InputTokens:       u.PromptTokens,
		OutputTokens:      u.CompletionTokens,
		CachedInputTokens: u.PromptTokensDetails.CachedTokens,
	}
}

func toFinish(reason string, hasToolCalls bool) core.FinishReason {
	switch reason {
	case "stop":
		return core.FinishStop
	case "length":
		return core.FinishLength
	case "tool_calls":
		return core.FinishToolCalls
	}
	if hasToolCalls {
		return core.FinishToolCalls
	}
	return core.FinishStop
}
