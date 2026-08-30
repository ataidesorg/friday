package wire

// Bedrock Converse adapter. Auth is SigV4 request signing via
// Options.Sign — never a bearer. Streaming falls back to one final delta:
// ConverseStream uses AWS event-stream binary framing that Ink has not
// implemented, and fake incremental chunks are worse than an honest lump
// (a recorded deviation).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ataidesorg/ink/internal/core"
)

// Bedrock speaks the Converse API: POST {base}/model/{modelId}/converse.
type Bedrock struct {
	opts     Options
	client   *http.Client
	endpoint string
}

// NewBedrock validates the options; Sign is mandatory — an unsigned
// Bedrock request can only fail, so building one is a config error.
func NewBedrock(o Options) (*Bedrock, error) {
	if err := validateOptions(o); err != nil {
		return nil, err
	}
	if o.Sign == nil {
		return nil, fmt.Errorf("%w: provider %s (bedrock) needs a request signer", core.ErrInvalidInput, o.ID)
	}
	return &Bedrock{
		opts:     o,
		client:   clientFor(o),
		endpoint: strings.TrimRight(o.BaseURL, "/") + "/model/" + url.PathEscape(o.Model) + "/converse",
	}, nil
}

// Descriptor reports identity and capabilities. Streaming stays false
// (single-delta fallback), StructuredOutput false (no response_format).
func (p *Bedrock) Descriptor() core.ProviderDescriptor {
	return core.ProviderDescriptor{
		ID:      p.opts.ID,
		Kind:    core.ProviderBedrock,
		Privacy: p.opts.Privacy,
		Capabilities: core.ProviderCapabilities{
			Streaming:        false,
			ToolCalling:      true,
			StructuredOutput: false,
			MaxContextTokens: p.opts.MaxContextTokens,
		},
		Health: core.ProviderHealth{State: core.HealthUnknown},
	}
}

// Complete sends one Converse request, signing each attempt.
func (p *Bedrock) Complete(ctx context.Context, req core.CompletionRequest) (core.CompletionResponse, error) {
	if len(req.OutputSchema) > 0 {
		return core.CompletionResponse{}, &core.NotImplementedError{
			Feature: "structured output on the bedrock converse wire",
		}
	}
	body, err := buildConverseRequest(req)
	if err != nil {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: encode request: %w", p.opts.ID, err)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: encode request: %w", p.opts.ID, err)
	}
	resp, err := doWithRetry(ctx, p.client, func(ctx context.Context) (*http.Request, error) {
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bodyReader(payload))
		if err != nil {
			return nil, err
		}
		hr.Header.Set("Content-Type", "application/json")
		for k, v := range p.opts.Headers {
			hr.Header.Set(k, v)
		}
		// Sign last: SigV4 covers every header already on the request.
		if err := p.opts.Sign(ctx, hr, payload); err != nil {
			return nil, fmt.Errorf("sign request: %w", err)
		}
		return hr, nil
	})
	if err != nil {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: %w", p.opts.ID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		drain(resp)
		return core.CompletionResponse{}, statusError(p.opts.ID, resp.StatusCode)
	}
	var out converseResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: decode response: %w", p.opts.ID, err)
	}
	cr := converseToCore(out)
	if p.opts.OnDelta != nil && cr.Content != "" {
		p.opts.OnDelta(cr.Content) // single final delta: recorded deviation
	}
	return cr, nil
}

// --- request mapping ---

type converseBlock struct {
	Text       string              `json:"text,omitempty"`
	ToolUse    *converseToolUse    `json:"toolUse,omitempty"`
	ToolResult *converseToolResult `json:"toolResult,omitempty"`
}

type converseToolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type converseToolResult struct {
	ToolUseID string          `json:"toolUseId"`
	Content   []converseBlock `json:"content"`
}

type converseMessage struct {
	Role    string          `json:"role"`
	Content []converseBlock `json:"content"`
}

type converseRequest struct {
	System          []converseBlock   `json:"system,omitempty"`
	Messages        []converseMessage `json:"messages"`
	InferenceConfig *struct {
		MaxTokens int `json:"maxTokens"`
	} `json:"inferenceConfig,omitempty"`
	ToolConfig *struct {
		Tools []converseTool `json:"tools"`
	} `json:"toolConfig,omitempty"`
}

type converseTool struct {
	ToolSpec struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		InputSchema struct {
			JSON json.RawMessage `json:"json"`
		} `json:"inputSchema"`
	} `json:"toolSpec"`
}

func buildConverseRequest(req core.CompletionRequest) (converseRequest, error) {
	var out converseRequest
	for _, m := range req.Messages {
		if len(m.Images) > 0 {
			// Refuse rather than silently drop the attachment.
			return out, fmt.Errorf("image attachments on the bedrock wire: %w", core.ErrNotImplemented)
		}
		switch m.Role {
		case core.RoleSystem:
			out.System = append(out.System, converseBlock{Text: m.Content})
		case core.RoleUser:
			out.Messages = append(out.Messages, converseMessage{
				Role: "user", Content: []converseBlock{{Text: m.Content}},
			})
		case core.RoleAssistant:
			var blocks []converseBlock
			if m.Content != "" {
				blocks = append(blocks, converseBlock{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				args := tc.Arguments
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				blocks = append(blocks, converseBlock{ToolUse: &converseToolUse{
					ToolUseID: string(tc.ID), Name: tc.Name, Input: args,
				}})
			}
			out.Messages = append(out.Messages, converseMessage{Role: "assistant", Content: blocks})
		case core.RoleTool:
			result := converseBlock{ToolResult: &converseToolResult{
				ToolUseID: string(m.ToolCallID),
				Content:   []converseBlock{{Text: m.Content}},
			}}
			// Converse alternates user/assistant strictly: consecutive tool
			// results fold into the same user message.
			if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == "user" && out.Messages[n-1].Content[0].ToolResult != nil {
				out.Messages[n-1].Content = append(out.Messages[n-1].Content, result)
				continue
			}
			out.Messages = append(out.Messages, converseMessage{Role: "user", Content: []converseBlock{result}})
		default:
			return converseRequest{}, fmt.Errorf("unsupported role %q", m.Role)
		}
	}
	if req.MaxOutputTokens > 0 {
		out.InferenceConfig = &struct {
			MaxTokens int `json:"maxTokens"`
		}{MaxTokens: req.MaxOutputTokens}
	}
	if len(req.Tools) > 0 {
		cfg := &struct {
			Tools []converseTool `json:"tools"`
		}{}
		for _, t := range req.Tools {
			var ct converseTool
			ct.ToolSpec.Name = t.Name
			ct.ToolSpec.Description = t.Description
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			ct.ToolSpec.InputSchema.JSON = schema
			cfg.Tools = append(cfg.Tools, ct)
		}
		out.ToolConfig = cfg
	}
	return out, nil
}

// --- response mapping ---

type converseResponse struct {
	Output struct {
		Message struct {
			Content []converseBlock `json:"content"`
		} `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
	} `json:"usage"`
}

func converseToCore(r converseResponse) core.CompletionResponse {
	var content strings.Builder
	var calls []core.ToolCall
	for _, b := range r.Output.Message.Content {
		if b.Text != "" {
			content.WriteString(b.Text)
		}
		if b.ToolUse != nil {
			calls = append(calls, core.ToolCall{
				ID:        core.ToolCallID(b.ToolUse.ToolUseID),
				Name:      b.ToolUse.Name,
				Arguments: b.ToolUse.Input,
			})
		}
	}
	return core.CompletionResponse{
		Content:   content.String(),
		ToolCalls: calls,
		Usage: core.Usage{
			InputTokens:  r.Usage.InputTokens,
			OutputTokens: r.Usage.OutputTokens,
		},
		Finish: bedrockFinish(r.StopReason, len(calls) > 0),
	}
}

func bedrockFinish(stopReason string, hasToolUse bool) core.FinishReason {
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
