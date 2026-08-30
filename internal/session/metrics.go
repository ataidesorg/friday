package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/redact"
)

const logsDir = "logs"

// metricsFile holds one Metric per line under <home>/logs/. Metrics are
// numbers plus config-level names (model, route, outcome) — never free text
// or tool output — so a stricter privacy mode has nothing to drop here; the
// transcript and the observability trail carry the text it gates.
const metricsFile = "metrics.jsonl"

// Metric is one turn's accounting, kept locally so chat quality can be
// studied later. It is transmitted nowhere.
type Metric struct {
	TS        time.Time       `json:"ts"`
	Session   string          `json:"session"`
	Model     string          `json:"model"`
	Route     string          `json:"route,omitempty"`
	Usage     core.Usage      `json:"usage"`
	Cost      core.CostReport `json:"cost"`
	LatencyMS int64           `json:"latency_ms"`
	ToolCalls int             `json:"tool_calls"`
	Outcome   string          `json:"outcome"`
}

// MetricsLog appends per-turn metrics to <home>/logs/metrics.jsonl, redacted
// and 0600, reusing the transcript's fail-closed sink discipline.
type MetricsLog struct {
	path string
	red  *redact.Redactor
}

// NewMetricsLog roots the metrics log at <home>/logs/metrics.jsonl. The
// redactor is required (fail closed): no line reaches disk without it.
func NewMetricsLog(home string, red *redact.Redactor) (*MetricsLog, error) {
	if red == nil {
		return nil, fmt.Errorf("%w: metrics log requires a redactor", core.ErrInvalidInput)
	}
	if home == "" {
		return nil, fmt.Errorf("%w: metrics log requires a home directory", core.ErrInvalidInput)
	}
	return &MetricsLog{path: filepath.Join(home, logsDir, metricsFile), red: red}, nil
}

// Append writes one metric as a redacted JSON line, creating the logs
// directory on first use.
func (l *MetricsLog) Append(m Metric) error {
	line, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal metric: %w", err)
	}
	safe := l.red.Redact(string(line))
	if err := os.MkdirAll(filepath.Dir(l.path), dirPerm); err != nil {
		return fmt.Errorf("create logs dir: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm) //nolint:gosec // metrics log under the Ink home
	if err != nil {
		return fmt.Errorf("open metrics log: %w", err)
	}
	if _, err := f.WriteString(safe + "\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("write metrics log: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close metrics log: %w", err)
	}
	return nil
}
