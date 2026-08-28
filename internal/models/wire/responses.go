package wire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ataidesorg/friday/internal/core"
)

// Responses speaks the OpenAI Responses dialect (POST /responses) used by
// Codex-era OpenAI endpoints and xAI.
type Responses struct {
	opts     Options
	client   *http.Client
	endpoint string
}

// NewResponses validates the options and returns the provider.
func NewResponses(o Options) (*Responses, error) {
	if err := validateOptions(o); err != nil {
		return nil, err
	}
	return &Responses{
		opts:     o,
		client:   clientFor(o),
		endpoint: strings.TrimRight(o.BaseURL, "/") + "/responses",
	}, nil
}

// Descriptor reports identity and capabilities.
func (p *Responses) Descriptor() core.ProviderDescriptor {
	return core.ProviderDescriptor{
		ID:      p.opts.ID,
		Kind:    core.ProviderResponses,
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

// Complete sends the request; with OnDelta set it streams.
func (p *Responses) Complete(ctx context.Context, req core.CompletionRequest) (core.CompletionResponse, error) {
	stream := p.opts.OnDelta != nil
	payload, err := json.Marshal(buildRespRequest(req, p.opts.Model, stream))
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
	var body respResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: decode response: %w", p.opts.ID, err)
	}
	return respToCore(body), nil
}

func (p *Responses) readStream(resp *http.Response) (core.CompletionResponse, error) {
	var out core.CompletionResponse
	completed := false
	err := readSSEEvents(resp.Body, func(event, data string) (bool, error) {
		switch event {
		case "response.output_text.delta":
			var d struct {
				Delta string `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &d); err != nil {
				return false, fmt.Errorf("malformed SSE payload: %w", err)
			}
			if d.Delta != "" {
				p.opts.OnDelta(d.Delta)
			}
			return false, nil
		case "response.completed":
			var d struct {
				Response respResponse `json:"response"`
			}
			if err := json.Unmarshal([]byte(data), &d); err != nil {
				return false, fmt.Errorf("malformed SSE payload: %w", err)
			}
			out = respToCore(d.Response)
			completed = true
			return true, nil
		case "response.failed", "error":
			return false, fmt.Errorf("upstream reported stream failure")
		default:
			return false, nil
		}
	})
	if err != nil {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: read stream: %w", p.opts.ID, err)
	}
	if !completed {
		return core.CompletionResponse{}, fmt.Errorf("provider %s: stream ended without response.completed", p.opts.ID)
	}
	return out, nil
}

// --- body shapes (Responses dialect) ---

type respRequest struct {
	Model           string          `json:"model"`
	Input           []respItem      `json:"input"`
	Tools           []respTool      `json:"tools,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	Text            *respTextConfig `json:"text,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
}

type respItem struct {
	Type      string           `json:"type,omitempty"`
	Role      string           `json:"role,omitempty"`
	Content   string           `json:"content,omitempty"`
	Images    []core.ImagePart `json:"-"` // rendered by MarshalJSON as input parts
	CallID    string           `json:"call_id,omitempty"`
	Name      string           `json:"name,omitempty"`
	Arguments string           `json:"arguments,omitempty"`
	Output    string           `json:"output,omitempty"`
}

// MarshalJSON switches content to the typed-part array form only when
// images ride along. Request-only: responses decode their own body types.
func (it respItem) MarshalJSON() ([]byte, error) {
	type plain respItem
	if len(it.Images) == 0 {
		return json.Marshal(plain(it))
	}
	parts := make([]map[string]any, 0, 1+len(it.Images))
	if it.Content != "" {
		parts = append(parts, map[string]any{"type": "input_text", "text": it.Content})
	}
	for _, img := range it.Images {
		parts = append(parts, map[string]any{
			"type":      "input_image",
			"image_url": "data:" + img.MediaType + ";base64," + img.Data,
		})
	}
	shadow := plain(it)
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

type respTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type respTextConfig struct {
	Format respFormat `json:"format"`
}

type respFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type respResponse struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []respOutputItem `json:"output"`
	Usage  *respUsage       `json:"usage"`
}

type respOutputItem struct {
	Type      string            `json:"type"`
	Role      string            `json:"role"`
	Content   []respContentPart `json:"content"`
	CallID    string            `json:"call_id"`
	Name      string            `json:"name"`
	Arguments string            `json:"arguments"`
}

type respContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type respUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// --- mapping ---

func buildRespRequest(req core.CompletionRequest, model string, stream bool) respRequest {
	out := respRequest{
		Model:           model,
		Input:           toRespInput(req.Messages),
		MaxOutputTokens: req.MaxOutputTokens,
		Stream:          stream,
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, respTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}
	if len(req.OutputSchema) > 0 {
		out.Text = &respTextConfig{Format: respFormat{
			Type:   "json_schema",
			Name:   "friday_output",
			Schema: req.OutputSchema,
			Strict: true,
		}}
	}
	return out
}

func toRespInput(msgs []core.Message) []respItem {
	out := make([]respItem, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case core.RoleTool:
			out = append(out, respItem{
				Type:   "function_call_output",
				CallID: string(m.ToolCallID),
				Output: m.Content,
			})
		case core.RoleAssistant:
			if m.Content != "" {
				out = append(out, respItem{Role: string(m.Role), Content: m.Content})
			}
			for _, tc := range m.ToolCalls {
				out = append(out, respItem{
					Type:      "function_call",
					CallID:    string(tc.ID),
					Name:      tc.Name,
					Arguments: string(tc.Arguments),
				})
			}
		default:
			out = append(out, respItem{Role: string(m.Role), Content: m.Content, Images: m.Images})
		}
	}
	return out
}

func respToCore(body respResponse) core.CompletionResponse {
	var content strings.Builder
	var calls []core.ToolCall
	for _, item := range body.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					content.WriteString(part.Text)
				}
			}
		case "function_call":
			calls = append(calls, core.ToolCall{
				ID:        core.ToolCallID(item.CallID),
				Name:      item.Name,
				Arguments: json.RawMessage(item.Arguments),
			})
		}
	}
	finish := core.FinishStop
	switch {
	case len(calls) > 0:
		finish = core.FinishToolCalls
	case body.Status == "incomplete" && body.IncompleteDetails != nil && body.IncompleteDetails.Reason == "max_output_tokens":
		finish = core.FinishLength
	}
	out := core.CompletionResponse{
		Content:   content.String(),
		ToolCalls: calls,
		Finish:    finish,
	}
	if body.Usage != nil {
		out.Usage = core.Usage{
			InputTokens:       body.Usage.InputTokens,
			OutputTokens:      body.Usage.OutputTokens,
			CachedInputTokens: body.Usage.InputTokensDetails.CachedTokens,
		}
	}
	return out
}
