package tools

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"

	"github.com/ataidesorg/friday/internal/core"
)

//go:embed schemas/*.json
var schemaFS embed.FS

// schema returns the embedded input schema for a tool, or nil when absent.
func schema(name string) json.RawMessage {
	b, err := schemaFS.ReadFile("schemas/" + name + ".json")
	if err != nil {
		return nil
	}
	return b
}

// decodeArgs validates raw arguments against the tool's embedded schema:
// required fields must be present and unknown fields are rejected.
// ponytail: required + strict decode, no schema library; add one if schemas grow conditionals.
func decodeArgs(name string, raw json.RawMessage, v any) error {
	s := schema(name)
	if s == nil {
		return fmt.Errorf("%w: tool %s has no input schema", core.ErrUnavailable, name)
	}
	var meta struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(s, &meta); err != nil {
		return fmt.Errorf("%w: tool %s schema: %w", core.ErrUnavailable, name, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return fmt.Errorf("%w: %s arguments must be a JSON object: %w", core.ErrInvalidInput, name, err)
	}
	for _, f := range meta.Required {
		if _, ok := present[f]; !ok {
			return fmt.Errorf("%w: %s requires %q", core.ErrInvalidInput, name, f)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %s arguments: %w", core.ErrInvalidInput, name, err)
	}
	return nil
}

// output packs a structured result and its model-facing text.
func output(text string, v any, used ...core.Capability) (core.ToolOutput, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return core.ToolOutput{}, fmt.Errorf("tool output: %w", err)
	}
	return core.ToolOutput{Content: text, Structured: b, CapabilitiesUsed: used}, nil
}
