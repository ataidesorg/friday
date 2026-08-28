// Package redact removes secrets from text before it reaches logs, events,
// memory, configuration output, or sandbox environments.
//
// Redaction is deterministic: the same input always yields the same output,
// and Redact is idempotent. Detection is a fixed list of RE2 patterns plus an
// optional list of exact literal secrets (for example values resolved from a
// keychain). The bias is toward redaction: false positives are accepted.
//
// Known false positive: any assignment whose name contains token, secret,
// key, password, passwd, or pwd followed by a value of MinLiteralLen or more
// characters is redacted, e.g. `TOKENIZER_PATH=/long/path` becomes
// `TOKENIZER_PATH=[REDACTED:assignment]`.
package redact

import (
	"regexp"
	"slices"
	"strings"
	"sync"
)

// MinLiteralLen is the shortest literal secret the redactor honours. Shorter
// literals are ignored because they would redact ordinary words.
const MinLiteralLen = 8

const (
	markerPrefix  = "[REDACTED:"
	markerSuffix  = "]"
	literalMarker = markerPrefix + "literal" + markerSuffix
)

type pattern struct {
	kind string
	re   *regexp.Regexp
	// replacement is the ReplaceAllString template; ${1} keeps a retained prefix.
	replacement string
}

func mark(kind string) string { return markerPrefix + kind + markerSuffix }

// builtin is ordered from most specific to most generic; the generic
// assignment rule runs last so specific kinds win the marker name.
var builtin = []pattern{
	{"private_key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), mark("private_key")},
	{"openai_key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), mark("openai_key")},
	{"github_token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}|\bgithub_pat_[A-Za-z0-9_]{22,}\b`), mark("github_token")},
	{"slack_token", regexp.MustCompile(`\bxox[abpors]-[A-Za-z0-9-]{10,}`), mark("slack_token")},
	{"aws_access_key", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), mark("aws_access_key")},
	{"google_api_key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35,}`), mark("google_api_key")},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), mark("jwt")},
	{"bearer_token", regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]{16,}`), "${1}" + mark("bearer_token")},
	{"assignment", regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:token|secret|key|password|passwd|pwd)[A-Za-z0-9_.-]*\s*[:=]\s*["']?)[^\s"'\[\]]{8,}`), "${1}" + mark("assignment")},
}

// Redactor replaces secrets with [REDACTED:<kind>] markers. A Redactor is
// immutable after New and safe for concurrent use.
type Redactor struct {
	mu       sync.RWMutex
	literals []string // longest first, each >= MinLiteralLen
}

// New returns a Redactor that applies the builtin patterns plus the given
// literal secrets. Literals shorter than MinLiteralLen are ignored. The
// caller's slice is never modified.
func New(literals ...string) *Redactor {
	kept := make([]string, 0, len(literals))
	for _, l := range literals {
		if len(l) >= MinLiteralLen && !slices.Contains(kept, l) {
			kept = append(kept, l)
		}
	}
	slices.SortStableFunc(kept, func(a, b string) int { return len(b) - len(a) })
	return &Redactor{literals: kept}
}

// AddLiteral registers more literal secrets after construction. Tokens
// resolved at call time are handed here the moment they exist so every
// later Redact call covers them. Short and duplicate literals are ignored.
// Safe for concurrent use with Redact and ContainsSecret.
func (r *Redactor) AddLiteral(literals ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range literals {
		if len(l) >= MinLiteralLen && !slices.Contains(r.literals, l) {
			r.literals = append(r.literals, l)
		}
	}
	slices.SortStableFunc(r.literals, func(a, b string) int { return len(b) - len(a) })
}

// Redact returns s with every literal secret (longest first) and every
// builtin pattern match replaced by a marker.
func (r *Redactor) Redact(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, l := range r.literals {
		s = strings.ReplaceAll(s, l, literalMarker)
	}
	for _, p := range builtin {
		s = p.re.ReplaceAllString(s, p.replacement)
	}
	return s
}

// ContainsSecret reports whether Redact would change s.
func (r *Redactor) ContainsSecret(s string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, l := range r.literals {
		if strings.Contains(s, l) {
			return true
		}
	}
	for _, p := range builtin {
		if p.re.MatchString(s) {
			return true
		}
	}
	return false
}
