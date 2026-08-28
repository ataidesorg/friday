package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/ataidesorg/friday/internal/redact"
)

// EventSchemaVersion is bumped on any incompatible change to Event JSON.
const EventSchemaVersion = 1

// Event is the versioned, attributable envelope around one payload.
type Event struct {
	SchemaVersion int
	ID            EventID
	At            time.Time
	Task          TaskID
	Run           RunID
	Seq           uint64
	Kind          EventKind
	Data          EventData
}

type eventJSON struct {
	SchemaVersion int             `json:"schema_version"`
	ID            EventID         `json:"id"`
	At            time.Time       `json:"at"`
	Task          TaskID          `json:"task"`
	Run           RunID           `json:"run"`
	Seq           uint64          `json:"seq"`
	Kind          EventKind       `json:"kind"`
	Data          json.RawMessage `json:"data"`
}

// NewEvent stamps a fresh ID and the current schema version.
func NewEvent(task TaskID, run RunID, seq uint64, at time.Time, data EventData) Event {
	return Event{SchemaVersion: EventSchemaVersion, ID: NewEventID(), At: at, Task: task, Run: run, Seq: seq, Kind: data.Kind(), Data: data}
}

// MarshalJSON encodes the envelope with the payload under "data".
func (e Event) MarshalJSON() ([]byte, error) {
	if e.Data == nil {
		return nil, fmt.Errorf("%w: event %s has no data", ErrInvalidInput, e.ID)
	}
	if e.Kind != e.Data.Kind() {
		return nil, fmt.Errorf("%w: event kind %q does not match payload %q", ErrInvalidInput, e.Kind, e.Data.Kind())
	}
	data, err := json.Marshal(e.Data)
	if err != nil {
		return nil, fmt.Errorf("encode event %s data: %w", e.ID, err)
	}
	return json.Marshal(eventJSON{e.SchemaVersion, e.ID, e.At, e.Task, e.Run, e.Seq, e.Kind, data})
}

// UnmarshalJSON decodes the payload by kind; unknown kinds and schema
// versions are rejected.
func (e *Event) UnmarshalJSON(b []byte) error {
	var raw eventJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("decode event: %w", err)
	}
	if raw.SchemaVersion != EventSchemaVersion {
		return fmt.Errorf("%w: event schema version %d, want %d", ErrInvalidInput, raw.SchemaVersion, EventSchemaVersion)
	}
	decode, ok := registry[raw.Kind]
	if !ok {
		return fmt.Errorf("%w: unknown event kind %q", ErrInvalidInput, raw.Kind)
	}
	data, err := decode(raw.Data)
	if err != nil {
		return fmt.Errorf("decode %s event %s: %w", raw.Kind, raw.ID, err)
	}
	*e = Event{raw.SchemaVersion, raw.ID, raw.At, raw.Task, raw.Run, raw.Seq, raw.Kind, data}
	return nil
}

func jsonUnmarshalStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// PrivacyMode selects how much free text survives redaction.
type PrivacyMode string

// Privacy modes.
const (
	PrivacyStandard PrivacyMode = "standard" // secrets scrubbed, text kept
	PrivacyMinimal  PrivacyMode = "minimal"  // secrets scrubbed, free text emptied
)

// ContentKeys are the JSON keys whose string values hold free text; minimal
// mode blanks them.
var ContentKeys = []string{"description", "input_summary", "output_summary", "summary", "justification", "reason", "message", "note", "command"}

// Redacted returns a copy of e with every string scrubbed by r. In minimal
// mode free-text fields are emptied as well. IDs, kinds, and numbers survive.
func (e Event) Redacted(r *redact.Redactor, mode PrivacyMode) (Event, error) {
	switch mode {
	case PrivacyStandard, PrivacyMinimal:
	case "":
		mode = PrivacyMinimal // fail closed: an unset mode drops free text
	default:
		return Event{}, fmt.Errorf("%w: unknown privacy mode %q", ErrInvalidInput, mode)
	}
	if r == nil {
		r = redact.New()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return Event{}, err
	}
	var tree any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&tree); err != nil {
		return Event{}, fmt.Errorf("redact event %s: %w", e.ID, err)
	}
	out, err := json.Marshal(scrub(tree, r, mode == PrivacyMinimal))
	if err != nil {
		return Event{}, fmt.Errorf("redact event %s: %w", e.ID, err)
	}
	var res Event
	if err := json.Unmarshal(out, &res); err != nil {
		return Event{}, fmt.Errorf("redact event %s: %w", e.ID, err)
	}
	return res, nil
}

// scrub returns a new tree; the input is never mutated.
func scrub(v any, r *redact.Redactor, minimal bool) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			if _, isStr := val.(string); isStr && minimal && slices.Contains(ContentKeys, k) {
				m[k] = ""
				continue
			}
			m[k] = scrub(val, r, minimal)
		}
		return m
	case []any:
		l := make([]any, len(t))
		for i, val := range t {
			l[i] = scrub(val, r, minimal)
		}
		return l
	case string:
		return r.Redact(t)
	default:
		return v
	}
}
