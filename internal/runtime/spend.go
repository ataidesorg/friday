package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ataidesorg/ink/internal/core"
)

// dayLayout formats the ledger's calendar day.
const dayLayout = "2006-01-02"

// SpendEntry is one ledger line: the actual model spend of one finished
// run on one calendar day.
type SpendEntry struct {
	Date      string         `json:"date"`
	Run       string         `json:"run"`
	USDMicros core.USDMicros `json:"usd_micros"`
}

// Spend enforces the money budgets that outlive one task: the per-session
// cap shared by every run in this process, and the per-day cap backed by a
// JSONL ledger on disk so it survives restarts. A zero cap disables that
// check; a nil *Spend in Deps disables both. Only totals live here — never
// credentials, prompts, or file contents.
type Spend struct {
	mu             sync.Mutex
	maxSession     core.USDMicros
	maxDay         core.USDMicros
	ledger         string
	session        core.USDMicros
	sessionUnknown bool
}

// NewSpend builds the tracker from the configured USD caps (0 disables a
// cap) and the ledger path ("" disables the day ledger).
func NewSpend(perSessionUSD, perDayUSD float64, ledgerPath string) (*Spend, error) {
	sess, err := core.USDFromFloat(perSessionUSD)
	if err != nil {
		return nil, fmt.Errorf("budgets.per_session_usd: %w", err)
	}
	day, err := core.USDFromFloat(perDayUSD)
	if err != nil {
		return nil, fmt.Errorf("budgets.per_day_usd: %w", err)
	}
	return &Spend{maxSession: sess, maxDay: day, ledger: ledgerPath}, nil
}

// AddSession folds one model call's actual cost into the session total. A
// nil cost — an unpriced model — makes the total unknown for the rest of
// the session, and the session cap then cannot be enforced.
func (s *Spend) AddSession(c *core.USDMicros) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c == nil || s.sessionUnknown {
		s.sessionUnknown = true
		return
	}
	sum, err := s.session.Add(*c)
	if err != nil {
		s.sessionUnknown = true
		return
	}
	s.session = sum
}

// SessionTotal reports the session spend so far and whether it is exact.
func (s *Spend) SessionTotal() (core.USDMicros, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session, !s.sessionUnknown
}

func (s *Spend) sessionCap() core.USDMicros { return s.maxSession }
func (s *Spend) dayCap() core.USDMicros     { return s.maxDay }

// DayTotal sums the ledger lines for one calendar day. A missing ledger is
// zero spend; a line that does not parse (or would overflow the total) is
// skipped and counted, never fatal — the caller warns about it.
func (s *Spend) DayTotal(day string) (total core.USDMicros, corrupt int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ledger == "" {
		return 0, 0, nil
	}
	f, err := os.Open(s.ledger)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("open spend ledger: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e SpendEntry
		if json.Unmarshal(line, &e) != nil || e.USDMicros < 0 {
			corrupt++
			continue
		}
		if e.Date != day {
			continue
		}
		sum, err := total.Add(e.USDMicros)
		if err != nil {
			corrupt++
			continue
		}
		total = sum
	}
	if err := sc.Err(); err != nil {
		return 0, corrupt, fmt.Errorf("read spend ledger: %w", err)
	}
	return total, corrupt, nil
}

// Commit appends one run's line to the ledger: a single O_APPEND write of
// a whole line with 0600 permissions, so concurrent ink processes never
// interleave records and the file stays owner-only.
func (s *Spend) Commit(e SpendEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ledger == "" {
		return nil
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode spend entry: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.ledger), 0o700); err != nil {
		return fmt.Errorf("spend ledger dir: %w", err)
	}
	f, err := os.OpenFile(s.ledger, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open spend ledger: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("append spend ledger: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close spend ledger: %w", err)
	}
	return nil
}
