package wire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/ataidesorg/friday/internal/core"
)

// anthropicVersion is the API version header every request must carry.
const anthropicVersion = "2023-06-01"

// anthropicDefaultMaxTokens fills the required max_tokens field when the
// request leaves MaxOutputTokens unset; the API rejects 0.
const anthropicDefaultMaxTokens = 4096

// AnthropicMessages speaks the Anthropic Messages dialect (POST /messages):
// x-api-key auth, content blocks, tool_use/tool_result, SSE event stream.
type AnthropicMessages struct {
	opts     Options
	client   *http.Client
	endpoint string
}

// NewAnthropicMessages validates the options and returns the provider.
func NewAnthropicMessages(o Options) (*AnthropicMessages, error) {
	if err := validateOptions(o); err != nil {
		return nil, err
	}
	return &AnthropicMessages{
		opts:     o,
		client:   clientFor(o),
		endpoint: strings.TrimRight(o.BaseURL, "/") + "/messages",
	}, nil
}

// Descriptor reports identity and capabilities. StructuredOutput stays
// false: the dialect has no response_format, and faking it with a forced
// tool is not implemented.
func (p *AnthropicMessages) Descriptor() core.ProviderDescriptor {
	return core.ProviderDescriptor{
		ID:      p.opts.ID,
		Kind:    core.ProviderAnthropic,
		Privacy: p.opts.Privacy,
		Capabilities: core.ProviderCapabilities{
			Streaming:        true,
			ToolCalling:      true,
			StructuredOutput: false,
			MaxContextTokens: p.opts.MaxContextTokens,
		},
		Health: core.ProviderHealth{State: core.HealthUnknown},
	}
}

// Complete sends the request; with OnDelta set it streams.
func (p *AnthropicMessages) Complete(ctx context.Context, req core.CompletionRequest) (core.CompletionResponse, error) {
	if len(req.OutputSchema) > 0 {
		return core.CompletionResponse{}, &core.NotImplementedError{
			Feature: "structured output on the anthropic_messages wire",
		}
	}
	stream := p.opts.OnDelta != nil
	payload, err := json.Marshal(buildAMRequest(req, p.opts.Model, stream))
	if err != nil {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: encode request: %w", p.opts.ID, err)
	}
	resp, err := postAuthed(ctx, p.opts, p.client, p.endpoint, payload, func(h http.Header, authValue string) {
		if authValue != "" {
			h.Set("x-api-key", authValue)
		}
		h.Set("anthropic-version", anthropicVersion)
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
	var body amResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: decode response: %w", p.opts.ID, err)
	}
	return amToCore(body), nil
}

func (p *AnthropicMessages) readStream(resp *http.Response) (core.CompletionResponse, error) {
	var (
		content strings.Builder
		usage   core.Usage
		finish  = core.FinishStop
		blocks  = map[int]*amStreamBlock{}
		stopped = false
	)
	err := readSSEEvents(resp.Body, func(event, data string) (bool, error) {
		switch event {
		case "message_start":
			var d struct {
				Message struct {
					Usage amUsage `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(data), &d); err != nil {
				return false, fmt.Errorf("malformed SSE payload: %w", err)
			}
			usage.InputTokens = d.Message.Usage.InputTokens
			usage.CachedInputTokens = d.Message.Usage.CacheReadInputTokens
		case "content_block_start":
			var d struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(data), &d); err != nil {
				return false, fmt.Errorf("malformed SSE payload: %w", err)
			}
			blocks[d.Index] = &amStreamBlock{
				kind: d.ContentBlock.Type,
				id:   d.ContentBlock.ID,
				name: d.ContentBlock.Name,
			}
		case "content_block_delta":
			var d struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &d); err != nil {
				return false, fmt.Errorf("malformed SSE payload: %w", err)
			}
			block := blocks[d.Index]
			if block == nil {
				return false, fmt.Errorf("delta for unknown content block %d", d.Index)
			}
			switch d.Delta.Type {
			case "text_delta":
				content.WriteString(d.Delta.Text)
				p.opts.OnDelta(d.Delta.Text)
			case "input_json_delta":
				block.args.WriteString(d.Delta.PartialJSON)
			}
		case "message_delta":
			var d struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage amUsage `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &d); err != nil {
				return false, fmt.Errorf("malformed SSE payload: %w", err)
			}
			if d.Delta.StopReason != "" {
				finish = amFinish(d.Delta.StopReason, anyToolUse(blocks))
			}
			if d.Usage.OutputTokens != 0 {
				usage.OutputTokens = d.Usage.OutputTokens
			}
		case "message_stop":
			stopped = true
			return true, nil
		case "error":
			return false, fmt.Errorf("upstream reported stream failure")
		}
		return false, nil
	})
	if err != nil {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: read stream: %w", p.opts.ID, err)
	}
	if !stopped {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: stream ended without message_stop", p.opts.ID)
	}
	return core.CompletionResponse{
		Content:   content.String(),
		ToolCalls: amStreamCalls(blocks),
		Usage:     usage,
		Finish:    finish,
	}, nil
}

// amStreamBlock accumulates one content block across SSE events by index.
type amStreamBlock struct {
	kind string
	id   string
	name string
	args strings.Builder
}

func anyToolUse(blocks map[int]*amStreamBlock) bool {
	for _, b := range blocks {
		if b.kind == "tool_use" {
			return true
		}
	}
	return false
}

func amStreamCalls(blocks map[int]*amStreamBlock) []core.ToolCall {
	indexes := make([]int, 0, len(blocks))
	for i, b := range blocks {
		if b.kind == "tool_use" {
			indexes = append(indexes, i)
		}
	}
	if len(indexes) == 0 {
		return nil
	}
	sort.Ints(indexes)
	out := make([]core.ToolCall, 0, len(indexes))
	for _, i := range indexes {
		b := blocks[i]
		out = append(out, core.ToolCall{
			ID:        core.ToolCallID(b.id),
			Name:      b.name,
			Arguments: json.RawMessage(b.args.String()),
		})
	}
	return out
}

// --- body shapes (Anthropic Messages dialect) ---

type amRequest struct {
	Model     string      `json:"model"`
	MaxTokens int         `json:"max_tokens"`
	System    string      `json:"system,omitempty"`
	Messages  []amMessage `json:"messages"`
	Tools     []amTool    `json:"tools,omitempty"`
	Stream    bool        `json:"stream,omitempty"`
}

type amMessage struct {
	Role    string    `json:"role"`
	Content []amBlock `json:"content"`
}

type amBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	Source    *amSource       `json:"source,omitempty"`
}

// amSource carries one base64 image block.
type amSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type amTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type amResponse struct {
	Content    []amRespBlock `json:"content"`
	StopReason string        `json:"stop_reason"`
	Usage      *amUsage      `json:"usage"`
}

type amRespBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type amUsage struct {
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
}

// --- mapping ---

func buildAMRequest(req core.CompletionRequest, model string, stream bool) amRequest {
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	out := amRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Stream:    stream,
	}
	var system []string
	for _, m := range req.Messages {
		switch m.Role {
		case core.RoleSystem:
			system = append(system, m.Content)
		case core.RoleTool:
			appendAMBlocks(&out.Messages, "user", []amBlock{{
				Type:      "tool_result",
				ToolUseID: string(m.ToolCallID),
				Content:   m.Content,
			}})
		case core.RoleAssistant:
			var blocks []amBlock
			if m.Content != "" {
				blocks = append(blocks, amBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, amBlock{
					Type:  "tool_use",
					ID:    string(tc.ID),
					Name:  tc.Name,
					Input: tc.Arguments,
				})
			}
			appendAMBlocks(&out.Messages, "assistant", blocks)
		default:
			blocks := []amBlock{{Type: "text", Text: m.Content}}
			for _, img := range m.Images {
				blocks = append(blocks, amBlock{Type: "image", Source: &amSource{
					Type: "base64", MediaType: img.MediaType, Data: img.Data,
				}})
			}
			appendAMBlocks(&out.Messages, "user", blocks)
		}
	}
	out.System = strings.Join(system, "\n")
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, amTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return out
}

// appendAMBlocks appends blocks, merging into the previous message when the
// role repeats — the dialect wants alternating roles, and consecutive tool
// results belong in one user message.
func appendAMBlocks(msgs *[]amMessage, role string, blocks []amBlock) {
	if len(blocks) == 0 {
		return
	}
	if n := len(*msgs); n > 0 && (*msgs)[n-1].Role == role {
		(*msgs)[n-1].Content = append((*msgs)[n-1].Content, blocks...)
		return
	}
	*msgs = append(*msgs, amMessage{Role: role, Content: blocks})
}

func amToCore(body amResponse) core.CompletionResponse {
	var content strings.Builder
	var calls []core.ToolCall
	for _, block := range body.Content {
		switch block.Type {
		case "text":
			content.WriteString(block.Text)
		case "tool_use":
			calls = append(calls, core.ToolCall{
				ID:        core.ToolCallID(block.ID),
				Name:      block.Name,
				Arguments: block.Input,
			})
		}
	}
	out := core.CompletionResponse{
		Content:   content.String(),
		ToolCalls: calls,
		Finish:    amFinish(body.StopReason, len(calls) > 0),
	}
	if body.Usage != nil {
		out.Usage = core.Usage{
			InputTokens:       body.Usage.InputTokens,
			OutputTokens:      body.Usage.OutputTokens,
			CachedInputTokens: body.Usage.CacheReadInputTokens,
		}
	}
	return out
}

func amFinish(stopReason string, hasToolUse bool) core.FinishReason {
	switch stopReason {
	case "end_turn", "stop_sequence":
		return core.FinishStop
	case "max_tokens":
		return core.FinishLength
	case "tool_use":
		return core.FinishToolCalls
	}
	if hasToolUse {
		return core.FinishToolCalls
	}
	return core.FinishStop
}
