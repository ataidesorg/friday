package core

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Goal objective and evidence bounds keep prompts scannable.
const (
	MaxGoalObjectiveBytes = 4000
	MaxGoalEvidenceBytes  = 4000
	MaxGoalReasonBytes    = 1000
	MinGoalBlockRepeats   = 3

	// DefaultGoalAutomaticTurns caps automatic model responses per goal.
	DefaultGoalAutomaticTurns = 25
	// DefaultGoalNoProgressTurns pauses after this many identical tool-free replies.
	DefaultGoalNoProgressTurns = 3
	// MinGoalWait is the shortest wait deadline the tools accept.
	MinGoalWait = 10 * time.Second

	// GoalContinuePrompt is the user message an idle auto-continue turn sends.
	GoalContinuePrompt = "Continue the session goal. Do not declare it done in prose. Call goal_complete with evidence of kind command, test, file, or eval."
)

// GoalStatus is the coarse session-goal state.
type GoalStatus string

// Goal statuses.
const (
	GoalActive   GoalStatus = "active"
	GoalPaused   GoalStatus = "paused"
	GoalBlocked  GoalStatus = "blocked"
	GoalWaiting  GoalStatus = "waiting"
	GoalComplete GoalStatus = "complete"
)

// GoalPauseCause explains why automatic work stopped.
type GoalPauseCause string

// Pause causes.
const (
	GoalCauseUser              GoalPauseCause = "user"
	GoalCauseContinuationLimit GoalPauseCause = "continuation_limit"
	GoalCauseNoProgress        GoalPauseCause = "no_progress"
	GoalCauseTokenBudget       GoalPauseCause = "token_budget"
	GoalCauseBlocked           GoalPauseCause = "blocked"
	GoalCauseWaiting           GoalPauseCause = "waiting"
)

// GoalEvidenceKind is how completion was proven.
type GoalEvidenceKind string

// Evidence kinds. Prose is not one of them.
const (
	GoalEvidenceCommand GoalEvidenceKind = "command"
	GoalEvidenceTest    GoalEvidenceKind = "test"
	GoalEvidenceFile    GoalEvidenceKind = "file"
	GoalEvidenceEval    GoalEvidenceKind = "eval"
)

// Goal is one session-scoped objective. It has no image fields: images are
// never persisted on a goal record.
type Goal struct {
	ID                GoalID           `json:"id"`
	Objective         string           `json:"objective"`
	Status            GoalStatus       `json:"status"`
	Usage             Usage            `json:"usage"`
	AutomaticTurns    int              `json:"automatic_turns"`
	MaxAutomaticTurns int              `json:"max_automatic_turns,omitempty"`
	NoProgress        int              `json:"no_progress,omitempty"`
	MaxNoProgress     int              `json:"max_no_progress,omitempty"`
	Fingerprint       string           `json:"fingerprint,omitempty"`
	PauseCause        GoalPauseCause   `json:"pause_cause,omitempty"`
	TokenBudget       int64            `json:"token_budget,omitempty"`
	WaitUntil         time.Time        `json:"wait_until,omitempty"`
	WaitReason        string           `json:"wait_reason,omitempty"`
	BlockReason       string           `json:"block_reason,omitempty"`
	BlockEvidence     string           `json:"block_evidence,omitempty"`
	RepeatedTurns     int              `json:"repeated_turns,omitempty"`
	EvidenceKind      GoalEvidenceKind `json:"evidence_kind,omitempty"`
	Evidence          string           `json:"evidence,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

// NewGoal starts an active goal. TokenBudget 0 means no token cap.
func NewGoal(objective string, now time.Time) (Goal, error) {
	obj := strings.TrimSpace(objective)
	if obj == "" {
		return Goal{}, fmt.Errorf("%w: goal objective is empty", ErrInvalidInput)
	}
	if len(obj) > MaxGoalObjectiveBytes {
		return Goal{}, fmt.Errorf("%w: goal objective exceeds %d bytes", ErrInvalidInput, MaxGoalObjectiveBytes)
	}
	return Goal{
		ID:                NewGoalID(),
		Objective:         obj,
		Status:            GoalActive,
		MaxAutomaticTurns: DefaultGoalAutomaticTurns,
		MaxNoProgress:     DefaultGoalNoProgressTurns,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

// Continues reports whether automatic work should keep going.
func (g Goal) Continues() bool { return g.Status == GoalActive }

// Open reports whether a goal is still in flight (not complete, not absent).
func (g Goal) Open() bool {
	switch g.Status {
	case GoalActive, GoalPaused, GoalBlocked, GoalWaiting:
		return true
	default:
		return false
	}
}

// Pause stops automatic work. Usage counters are kept.
func (g Goal) Pause(cause GoalPauseCause, now time.Time) (Goal, error) {
	if !g.Open() {
		return Goal{}, fmt.Errorf("%w: goal %s cannot pause from %s", ErrInvalidInput, g.ID, g.Status)
	}
	if g.Status != GoalActive && g.Status != GoalWaiting {
		return Goal{}, fmt.Errorf("%w: only an active or waiting goal can pause", ErrInvalidInput)
	}
	next := g
	next.Status = GoalPaused
	next.PauseCause = cause
	next.UpdatedAt = now
	return next, nil
}

// Resume restarts a stopped goal without resetting usage.
func (g Goal) Resume(now time.Time) (Goal, error) {
	switch g.Status {
	case GoalPaused, GoalBlocked, GoalWaiting:
	default:
		return Goal{}, fmt.Errorf("%w: goal %s cannot resume from %s", ErrInvalidInput, g.ID, g.Status)
	}
	next := g
	next.Status = GoalActive
	next.PauseCause = ""
	next.WaitReason = ""
	next.WaitUntil = time.Time{}
	next.BlockReason = ""
	next.BlockEvidence = ""
	next.UpdatedAt = now
	return next, nil
}

// Edit replaces the objective and keeps usage counters.
func (g Goal) Edit(objective string, now time.Time) (Goal, error) {
	if !g.Open() {
		return Goal{}, fmt.Errorf("%w: goal %s cannot edit from %s", ErrInvalidInput, g.ID, g.Status)
	}
	obj := strings.TrimSpace(objective)
	if obj == "" {
		return Goal{}, fmt.Errorf("%w: goal objective is empty", ErrInvalidInput)
	}
	if len(obj) > MaxGoalObjectiveBytes {
		return Goal{}, fmt.Errorf("%w: goal objective exceeds %d bytes", ErrInvalidInput, MaxGoalObjectiveBytes)
	}
	next := g
	next.Objective = obj
	next.UpdatedAt = now
	return next, nil
}

// WithTokenBudget sets an absolute token cap. 0 clears it.
func (g Goal) WithTokenBudget(n int64, now time.Time) (Goal, error) {
	if n < 0 {
		return Goal{}, fmt.Errorf("%w: token budget must not be negative", ErrInvalidInput)
	}
	next := g
	next.TokenBudget = n
	next.UpdatedAt = now
	return next, nil
}

// Complete records evidence-backed done. Prose is not evidence.
func (g Goal) Complete(kind GoalEvidenceKind, evidence string, now time.Time) (Goal, error) {
	if g.Status != GoalActive && g.Status != GoalWaiting {
		return Goal{}, fmt.Errorf("%w: goal %s cannot complete from %s", ErrInvalidInput, g.ID, g.Status)
	}
	if !validEvidenceKind(kind) {
		return Goal{}, fmt.Errorf("%w: evidence kind %q (want command, test, file, or eval)", ErrInvalidInput, kind)
	}
	ev := strings.TrimSpace(evidence)
	if ev == "" {
		return Goal{}, fmt.Errorf("%w: goal completion requires evidence", ErrInvalidInput)
	}
	if len(ev) > MaxGoalEvidenceBytes {
		return Goal{}, fmt.Errorf("%w: evidence exceeds %d bytes", ErrInvalidInput, MaxGoalEvidenceBytes)
	}
	next := g
	next.Status = GoalComplete
	next.EvidenceKind = kind
	next.Evidence = ev
	next.PauseCause = ""
	next.UpdatedAt = now
	return next, nil
}

// Block records a repeated impasse.
func (g Goal) Block(reason, evidence string, repeated int, now time.Time) (Goal, error) {
	if g.Status != GoalActive && g.Status != GoalWaiting {
		return Goal{}, fmt.Errorf("%w: goal %s cannot block from %s", ErrInvalidInput, g.ID, g.Status)
	}
	r := strings.TrimSpace(reason)
	ev := strings.TrimSpace(evidence)
	if r == "" || ev == "" {
		return Goal{}, fmt.Errorf("%w: goal_blocked needs a reason and evidence", ErrInvalidInput)
	}
	if len(r) > MaxGoalReasonBytes {
		return Goal{}, fmt.Errorf("%w: block reason exceeds %d bytes", ErrInvalidInput, MaxGoalReasonBytes)
	}
	if len(ev) > MaxGoalEvidenceBytes {
		return Goal{}, fmt.Errorf("%w: block evidence exceeds %d bytes", ErrInvalidInput, MaxGoalEvidenceBytes)
	}
	if repeated < MinGoalBlockRepeats {
		return Goal{}, fmt.Errorf("%w: goal_blocked needs at least %d repeated turns", ErrInvalidInput, MinGoalBlockRepeats)
	}
	next := g
	next.Status = GoalBlocked
	next.PauseCause = GoalCauseBlocked
	next.BlockReason = r
	next.BlockEvidence = ev
	next.RepeatedTurns = repeated
	next.UpdatedAt = now
	return next, nil
}

// Wait pauses automatic continuation until an external wake or deadline.
func (g Goal) Wait(reason string, until time.Time, now time.Time) (Goal, error) {
	if g.Status != GoalActive {
		return Goal{}, fmt.Errorf("%w: goal %s cannot wait from %s", ErrInvalidInput, g.ID, g.Status)
	}
	r := strings.TrimSpace(reason)
	if r == "" {
		return Goal{}, fmt.Errorf("%w: goal_wait needs a reason", ErrInvalidInput)
	}
	if len(r) > MaxGoalReasonBytes {
		return Goal{}, fmt.Errorf("%w: wait reason exceeds %d bytes", ErrInvalidInput, MaxGoalReasonBytes)
	}
	if !until.IsZero() && !until.After(now) {
		return Goal{}, fmt.Errorf("%w: wait deadline must be in the future", ErrInvalidInput)
	}
	next := g
	next.Status = GoalWaiting
	next.PauseCause = GoalCauseWaiting
	next.WaitReason = r
	next.WaitUntil = until
	next.UpdatedAt = now
	return next, nil
}

// RecordTurn accounts one finished model turn and may pause on caps.
// hadTools true resets the no-progress streak.
func (g Goal) RecordTurn(usage Usage, fingerprint string, hadTools bool, now time.Time) Goal {
	if g.Status != GoalActive {
		return g
	}
	next := g
	next.Usage = next.Usage.Add(usage)
	next.AutomaticTurns++
	next.UpdatedAt = now
	fp := normalizeFingerprint(fingerprint)
	switch {
	case hadTools:
		next.NoProgress = 0
		next.Fingerprint = ""
	case fp == "":
		next.NoProgress++
	case fp == next.Fingerprint:
		next.NoProgress++
	default:
		next.Fingerprint = fp
		next.NoProgress = 1
	}
	maxTurns := next.MaxAutomaticTurns
	if maxTurns <= 0 {
		maxTurns = DefaultGoalAutomaticTurns
	}
	maxNP := next.MaxNoProgress
	if maxNP <= 0 {
		maxNP = DefaultGoalNoProgressTurns
	}
	switch {
	case next.TokenBudget > 0 && next.Usage.Total() >= next.TokenBudget:
		paused, err := next.Pause(GoalCauseTokenBudget, now)
		if err == nil {
			return paused
		}
	case next.NoProgress >= maxNP:
		paused, err := next.Pause(GoalCauseNoProgress, now)
		if err == nil {
			return paused
		}
	case next.AutomaticTurns >= maxTurns:
		paused, err := next.Pause(GoalCauseContinuationLimit, now)
		if err == nil {
			return paused
		}
	}
	return next
}

// Contract is the assemble snippet pinned across compaction.
func (g Goal) Contract() string {
	if g.ID == "" || g.Objective == "" || g.Status == GoalComplete {
		return ""
	}
	return fmt.Sprintf(`<goal id=%q status=%q>
Objective: %s
Do not declare this job done in prose. Call goal_complete with this goal_id and evidence of kind command, test, file, or eval. If you are stuck after repeated attempts, call goal_blocked. To wait on an external event, call goal_wait.
</goal>`, g.ID, g.Status, g.Objective)
}

func validEvidenceKind(k GoalEvidenceKind) bool {
	switch k {
	case GoalEvidenceCommand, GoalEvidenceTest, GoalEvidenceFile, GoalEvidenceEval:
		return true
	default:
		return false
	}
}

func normalizeFingerprint(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsControl(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// ParseTokenBudget accepts integers and k/m suffixes (100k, 1.5m).
func ParseTokenBudget(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("%w: empty token budget", ErrInvalidInput)
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "m"):
		mult = 1_000_000
		s = strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "k"):
		mult = 1_000
		s = strings.TrimSuffix(s, "k")
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: token budget %q", ErrInvalidInput, s)
	}
	v := int64(n * float64(mult))
	if v <= 0 {
		return 0, fmt.Errorf("%w: token budget must be positive", ErrInvalidInput)
	}
	return v, nil
}
