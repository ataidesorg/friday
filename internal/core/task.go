package core

import (
	"fmt"
	"strings"
	"time"
)

// PrincipalKind says what kind of actor a Principal is.
type PrincipalKind string

// Principal kinds.
const (
	PrincipalUser   PrincipalKind = "user"
	PrincipalAgent  PrincipalKind = "agent"
	PrincipalSystem PrincipalKind = "system"
)

// Principal is an attributable actor (a human, the agent, or the harness).
type Principal struct {
	Kind PrincipalKind `json:"kind"`
	Name string        `json:"name"`
}

// TaskBudget caps a task; a zero field means unset.
type TaskBudget struct {
	MaxCost      USDMicros     `json:"max_cost,omitempty"`
	MaxWallClock time.Duration `json:"max_wall_clock,omitempty"`
	MaxToolCalls int           `json:"max_tool_calls,omitempty"`
}

// MaxDescriptionBytes bounds a task description.
const MaxDescriptionBytes = 65536

// Task is a unit of requested work.
type Task struct {
	ID          TaskID      `json:"id"`
	Description string      `json:"description"`
	Harness     HarnessKind `json:"harness"`
	Profile     ProfileID   `json:"profile"`
	Session     SessionID   `json:"session"`
	CreatedBy   Principal   `json:"created_by"`
	Budget      TaskBudget  `json:"budget"`
}

// NewTask validates the description and returns a Task with a fresh ID.
func NewTask(description string, harness HarnessKind, profile ProfileID, session SessionID, by Principal) (Task, error) {
	if strings.TrimSpace(description) == "" {
		return Task{}, fmt.Errorf("%w: task description is empty", ErrInvalidInput)
	}
	if len(description) > MaxDescriptionBytes {
		return Task{}, fmt.Errorf("%w: task description exceeds %d bytes", ErrInvalidInput, MaxDescriptionBytes)
	}
	return Task{ID: NewTaskID(), Description: description, Harness: harness, Profile: profile, Session: session, CreatedBy: by}, nil
}

// Run is one attempt at a task.
type Run struct {
	ID         RunID     `json:"id"`
	Task       TaskID    `json:"task"`
	Attempt    int       `json:"attempt"`
	State      TaskState `json:"state"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// NewRun starts attempt number attempt of a task.
func NewRun(task TaskID, attempt int, now time.Time) Run {
	return Run{ID: NewRunID(), Task: task, Attempt: attempt, State: InitialState(), StartedAt: now}
}

// Transition returns a copy of r with the transition applied; FinishedAt is
// set when the new state is terminal. The receiver is never modified.
func (r Run) Transition(t Transition, now time.Time) (Run, error) {
	next, err := r.State.Apply(t)
	if err != nil {
		return r, err
	}
	r.State = next
	if next.Terminal() {
		r.FinishedAt = now
	}
	return r, nil
}

// Elapsed returns how long the run has been (or was) running.
func (r Run) Elapsed(now time.Time) time.Duration {
	if !r.FinishedAt.IsZero() {
		return r.FinishedAt.Sub(r.StartedAt)
	}
	return now.Sub(r.StartedAt)
}
