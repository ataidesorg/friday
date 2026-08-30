package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ataidesorg/ink/internal/core"
)

// CheckpointSchema is bumped on incompatible checkpoint JSON.
const CheckpointSchema = 1

// Checkpoint is a run's recoverable state. Images are stripped: they are
// never persisted (same rule as the session store).
type Checkpoint struct {
	Schema   int                     `json:"schema"`
	Run      core.Run                `json:"run"`
	Messages []core.Message          `json:"messages"`
	Usage    core.Usage              `json:"usage"`
	Seq      uint64                  `json:"seq"`
	Calls    int                     `json:"calls"`
	Verified bool                    `json:"verified"`
	Verify   string                  `json:"verify,omitempty"`
	Last     core.CompletionResponse `json:"last"`
	Cost     core.CostReport         `json:"cost"`
	Summary  string                  `json:"summary,omitempty"`
}

// CheckpointPath is <root>/.ink/local/runs/<run>/checkpoint.json.
func CheckpointPath(projectRoot string, run core.RunID) string {
	return filepath.Join(projectRoot, ".ink", "local", "runs", string(run), "checkpoint.json")
}

// SaveCheckpoint writes c to path, creating parent directories. Images on
// messages are dropped before encode.
func SaveCheckpoint(path string, c Checkpoint) error {
	if path == "" {
		return fmt.Errorf("%w: checkpoint path is empty", core.ErrInvalidInput)
	}
	c.Schema = CheckpointSchema
	c.Messages = stripImages(c.Messages)
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadCheckpoint reads a checkpoint file. A missing file is ErrNotFound.
func LoadCheckpoint(path string) (Checkpoint, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is the run-local checkpoint the caller named
	if err != nil {
		if os.IsNotExist(err) {
			return Checkpoint{}, fmt.Errorf("%w: %s", core.ErrNotFound, path)
		}
		return Checkpoint{}, err
	}
	var c Checkpoint
	if err := json.Unmarshal(b, &c); err != nil {
		return Checkpoint{}, fmt.Errorf("%w: checkpoint: %w", core.ErrInvalidInput, err)
	}
	if c.Schema != CheckpointSchema {
		return Checkpoint{}, fmt.Errorf("%w: checkpoint schema %d, want %d", core.ErrInvalidInput, c.Schema, CheckpointSchema)
	}
	return c, nil
}

func (s *state) snapshot() Checkpoint {
	return Checkpoint{
		Schema:   CheckpointSchema,
		Run:      s.run,
		Messages: stripImages(s.msgs),
		Usage:    s.usage,
		Seq:      s.seq,
		Calls:    s.calls,
		Verified: s.verified,
		Verify:   s.verify,
		Last:     s.last,
		Cost:     s.cost,
		Summary:  s.summary,
	}
}

func (s *state) hydrate(c Checkpoint) {
	s.run = c.Run
	s.msgs = c.Messages
	s.usage = c.Usage
	s.seq = c.Seq
	s.calls = c.Calls
	s.verified = c.Verified
	s.verify = c.Verify
	s.last = c.Last
	s.cost = c.Cost
	s.summary = c.Summary
}

func (s *state) persistCheckpoint() error {
	if s.in.CheckpointPath == "" {
		return nil
	}
	return SaveCheckpoint(s.in.CheckpointPath, s.snapshot())
}

func stripImages(msgs []core.Message) []core.Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]core.Message, len(msgs))
	for i, m := range msgs {
		m.Images = nil
		out[i] = m
	}
	return out
}
