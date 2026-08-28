package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/ataidesorg/friday/internal/redact"
)

// MemoryCategory is the kind of knowledge a record holds.
type MemoryCategory string

// Memory categories.
const (
	MemorySemantic    MemoryCategory = "semantic"
	MemoryEpisodic    MemoryCategory = "episodic"
	MemoryProcedural  MemoryCategory = "procedural"
	MemoryProject     MemoryCategory = "project"
	MemoryOperational MemoryCategory = "operational"
	MemoryWorking     MemoryCategory = "working"
)

// Sensitivity bounds where a record may flow. Secret is never storable.
type Sensitivity string

// Sensitivity levels.
const (
	SensitivityPublic   Sensitivity = "public"
	SensitivityInternal Sensitivity = "internal"
	SensitivityPersonal Sensitivity = "personal"
	SensitivitySecret   Sensitivity = "secret"
)

func (s Sensitivity) rank() int {
	switch s {
	case SensitivityPublic:
		return 0
	case SensitivityInternal:
		return 1
	case SensitivityPersonal:
		return 2
	case SensitivitySecret:
		return 3
	default:
		return 4
	}
}

// WithinSensitivityCap reports whether got may be stored or retrieved under
// cap. Secret is never allowed. An empty cap means anything but secret.
func WithinSensitivityCap(limit, got Sensitivity) bool {
	if got == SensitivitySecret || got.rank() > 3 {
		return false
	}
	if limit == "" {
		return true
	}
	return got.rank() <= limit.rank()
}

// OriginKind says how a memory came to exist.
type OriginKind string

// Origin kinds.
const (
	OriginUserStated    OriginKind = "user_stated"
	OriginDeterministic OriginKind = "deterministic"
	OriginModelInferred OriginKind = "model_inferred"
)

// Provenance is the attributable source of a memory.
type Provenance struct {
	Origin OriginKind `json:"origin"`
	Run    RunID      `json:"run,omitempty"`
	Source string     `json:"source,omitempty"`
	By     Principal  `json:"by"`
}

// CandidateStatus is the review outcome of a candidate.
type CandidateStatus string

// Candidate statuses.
const (
	CandidatePending  CandidateStatus = "pending"
	CandidateAccepted CandidateStatus = "accepted"
	CandidateRejected CandidateStatus = "rejected"
)

// Confidence is a coarse trust level for a remembered fact.
type Confidence string

// Confidence levels.
const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

// MemoryCandidate is a proposed memory awaiting review.
type MemoryCandidate struct {
	ID          CandidateID     `json:"id"`
	Namespace   string          `json:"namespace"`
	Category    MemoryCategory  `json:"category"`
	Content     string          `json:"content"`
	Provenance  Provenance      `json:"provenance"`
	Confidence  Confidence      `json:"confidence"`
	Sensitivity Sensitivity     `json:"sensitivity"`
	Status      CandidateStatus `json:"status"`
	CreatedAt   time.Time       `json:"created_at"`
}

// NewMemoryCandidate builds a pending candidate, refusing secret content.
func NewMemoryCandidate(ns string, cat MemoryCategory, content string, prov Provenance, conf Confidence, sens Sensitivity, r *redact.Redactor, now time.Time) (MemoryCandidate, error) {
	if strings.TrimSpace(ns) == "" || strings.TrimSpace(content) == "" {
		return MemoryCandidate{}, fmt.Errorf("%w: namespace and content are required", ErrInvalidInput)
	}
	if sens == SensitivitySecret {
		return MemoryCandidate{}, fmt.Errorf("%w: sensitivity %q is not storable", ErrSecretContent, sens)
	}
	if r == nil {
		r = redact.New()
	}
	if r.ContainsSecret(content) {
		return MemoryCandidate{}, fmt.Errorf("%w: candidate content", ErrSecretContent)
	}
	id, err := newID()
	if err != nil {
		return MemoryCandidate{}, err
	}
	return MemoryCandidate{
		ID: CandidateID(id), Namespace: ns, Category: cat, Content: content,
		Provenance: prov, Confidence: conf, Sensitivity: sens,
		Status: CandidatePending, CreatedAt: now,
	}, nil
}
