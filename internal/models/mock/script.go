// Package mock is a scripted core.ModelProvider that replays turns from a
// fixture file. It lets a run go end to end with no network, no key, and
// byte-identical output across runs.
package mock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/ataidesorg/friday/internal/core"
)

// Turn is one scripted assistant reply.
type Turn struct {
	Content   string            `json:"content,omitempty"`
	ToolCalls []core.ToolCall   `json:"tool_calls,omitempty"`
	Finish    core.FinishReason `json:"finish"`
	Usage     core.Usage        `json:"usage"`
	// Match is an optional substring the last request message must contain.
	Match string `json:"match,omitempty"`
}

// Script is a model name plus the ordered turns it replays.
type Script struct {
	Model string `json:"model"`
	Turns []Turn `json:"turns"`
}

// ErrScriptExhausted is returned when every turn has been played.
var ErrScriptExhausted = errors.New("mock script exhausted")

// ErrScriptMismatch is returned when a turn's Match substring is absent.
var ErrScriptMismatch = errors.New("mock script mismatch")

var validFinish = map[core.FinishReason]bool{
	core.FinishStop: true, core.FinishLength: true, core.FinishToolCalls: true, core.FinishError: true,
}

// LoadScript reads and validates a script file. Unknown fields, empty turn
// lists, and malformed turns are rejected with core.ErrInvalidInput.
func LoadScript(path string) (Script, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // script path is an operator-supplied fixture
	if err != nil {
		return Script{}, fmt.Errorf("mock script: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var s Script
	if err := dec.Decode(&s); err != nil {
		return Script{}, fmt.Errorf("%w: mock script %s: %w", core.ErrInvalidInput, path, err)
	}
	if err := s.validate(); err != nil {
		return Script{}, fmt.Errorf("%w: mock script %s: %w", core.ErrInvalidInput, path, err)
	}
	return s, nil
}

func (s Script) validate() error {
	if s.Model == "" {
		return errors.New("model is empty")
	}
	if len(s.Turns) == 0 {
		return errors.New("no turns")
	}
	for i, t := range s.Turns {
		if !validFinish[t.Finish] {
			return fmt.Errorf("turn %d: unknown finish %q", i, t.Finish)
		}
		if (t.Finish == core.FinishToolCalls) != (len(t.ToolCalls) > 0) {
			return fmt.Errorf("turn %d: finish %q disagrees with %d tool calls", i, t.Finish, len(t.ToolCalls))
		}
		for j, c := range t.ToolCalls {
			if c.ID == "" || c.Name == "" {
				return fmt.Errorf("turn %d call %d: id and name are required", i, j)
			}
			var buf bytes.Buffer
			if err := json.Compact(&buf, c.Arguments); err != nil {
				return fmt.Errorf("turn %d call %d: arguments: %w", i, j, err)
			}
			s.Turns[i].ToolCalls[j].Arguments = buf.Bytes()
		}
	}
	return nil
}
