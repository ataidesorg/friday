package mock

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/ataidesorg/friday/internal/core"
)

// Provider replays a Script one turn per Complete call.
type Provider struct {
	mu     sync.Mutex
	script Script
	next   int
}

// New wraps a validated Script.
func New(s Script) *Provider { return &Provider{script: s} }

// Descriptor reports a local, healthy, tool-calling provider with no prices.
func (p *Provider) Descriptor() core.ProviderDescriptor {
	return core.ProviderDescriptor{
		ID:           "mock",
		Kind:         core.ProviderMock,
		Privacy:      core.PrivacyLocal,
		Capabilities: core.ProviderCapabilities{ToolCalling: true},
		Health:       core.ProviderHealth{State: core.HealthHealthy},
	}
}

// Complete returns the next scripted turn. A mismatch, cancelled context, or
// wrong model does not advance the script.
func (p *Provider) Complete(ctx context.Context, req core.CompletionRequest) (core.CompletionResponse, error) {
	if err := ctx.Err(); err != nil {
		return core.CompletionResponse{}, err
	}
	if req.Model != "" && req.Model != p.script.Model {
		return core.CompletionResponse{}, fmt.Errorf("%w: mock script is for model %q, got %q", core.ErrInvalidInput, p.script.Model, req.Model)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.next >= len(p.script.Turns) {
		return core.CompletionResponse{}, fmt.Errorf("%w after %d turns", ErrScriptExhausted, p.next)
	}
	t := p.script.Turns[p.next]
	if t.Match != "" && !strings.Contains(lastContent(req.Messages), t.Match) {
		return core.CompletionResponse{}, fmt.Errorf("%w: turn %d wants %q in the last message", ErrScriptMismatch, p.next, t.Match)
	}
	p.next++
	return core.CompletionResponse{
		Content:   t.Content,
		ToolCalls: append([]core.ToolCall(nil), t.ToolCalls...),
		Usage:     t.Usage,
		Finish:    t.Finish,
	}, nil
}

func lastContent(msgs []core.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1].Content
}
