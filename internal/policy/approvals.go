package policy

import (
	"strings"
	"sync"

	"github.com/ataidesorg/friday/internal/core"
)

// Approvals remembers session-scoped human decisions for one run or session.
// ponytail: in-memory only; persist with the session if approvals must survive a restart.
type Approvals struct {
	mu      sync.RWMutex
	session map[string]core.ApprovalResolution
}

// NewApprovals returns an empty store.
func NewApprovals() *Approvals { return &Approvals{session: map[string]core.ApprovalResolution{}} }

// Key identifies what an approval covers: tool, risk, scope kind, and the
// exact path or argv. A command approval covers one full argv — keying on
// the program name alone would let `git status` unlock every git command.
func (*Approvals) Key(req core.CapabilityRequest) string {
	s := req.Capability.Scope
	var target string
	switch s.Kind {
	case core.ScopePath:
		target = s.Path
	case core.ScopeCommand:
		target = strings.Join(s.Argv, "\x00") // NUL keeps distinct argvs distinct
	case core.ScopeNetwork:
		target = s.Host
	case core.ScopeEnv, core.ScopeSecret:
		target = s.Name
	}
	return strings.Join([]string{req.Tool, string(req.Capability.Risk), string(s.Kind), target}, "|")
}

// Lookup returns a stored session-scoped decision for an identical key.
func (a *Approvals) Lookup(req core.CapabilityRequest) (core.ApprovalResolution, bool) {
	if a == nil {
		return core.ApprovalResolution{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	res, ok := a.session[a.Key(req)]
	return res, ok
}

// Record stores a session-scoped decision; once-scoped decisions are not kept.
func (a *Approvals) Record(req core.CapabilityRequest, res core.ApprovalResolution) {
	if a == nil || res.Scope != core.ApprovalSession {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.session[a.Key(req)] = res
}
