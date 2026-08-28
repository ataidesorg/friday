package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// gate decides which keys of a repository layer (project or project_local)
// merge. Keys outside ProjectLayerGatedPrefixes always merge. Opt-in risk
// flags never merge (RejectAllowlist). Everything else merges only when the
// trust store holds a "trusted" entry for this exact file content; otherwise
// the keys are recorded as rejected with the reason and, when a prompt is
// available and no decision covers this content, the owner is asked. An
// answer binds to the file content it was given for, so editing a declined
// file asks again instead of staying rejected forever.
type gate struct {
	store  *TrustStore
	prompt TrustPrompt
	now    func() time.Time
}

// apply returns the keys of m that may merge; rejected leaves are recorded
// in prov. data is the raw file content the hash is taken from.
func (g gate) apply(m map[string]any, from Source, data []byte, prov Provenance) (map[string]any, error) {
	kept := map[string]any{}
	var gated, gatedValues = []string{}, map[string]any{}
	for key, value := range flatten(m, "") {
		switch {
		case isRiskFlag(key):
			prov[key] = append(prov[key], Entry{Source: from, Value: value, Rejected: true, Reason: RejectAllowlist})
		case projectMaySet(key):
			next, err := setNested(kept, splitKey(key), value)
			if err != nil {
				return nil, fmt.Errorf("parse %s config %s: %w", from.Layer, from.Path, err)
			}
			kept = next
		default:
			gated = append(gated, key)
			gatedValues[key] = value
		}
	}
	if len(gated) == 0 {
		return kept, nil
	}
	sort.Strings(gated)
	reason, err := g.decide(from.Path, data, gated)
	if err != nil {
		return nil, err
	}
	for _, key := range gated {
		if reason != "" {
			prov[key] = append(prov[key], Entry{Source: from, Value: gatedValues[key], Rejected: true, Reason: reason})
			continue
		}
		next, err := setNested(kept, splitKey(key), gatedValues[key])
		if err != nil {
			return nil, fmt.Errorf("parse %s config %s: %w", from.Layer, from.Path, err)
		}
		kept = next
	}
	return kept, nil
}

// decide returns "" when the file is trusted, else the reject reason.
func (g gate) decide(path string, data []byte, keys []string) (RejectReason, error) {
	if g.store == nil {
		return RejectUntrusted, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	hash := fileSHA256(data)
	entry, found, err := g.store.Lookup(abs)
	if err != nil {
		return "", err
	}
	if found && entry.SHA256 == hash {
		if entry.Decision == TrustUntrusted {
			return RejectDeclined, nil
		}
		return "", nil
	}
	if g.prompt == nil {
		if found {
			return RejectHashChanged, nil
		}
		return RejectUntrusted, nil
	}
	decision, err := g.prompt(path, keys)
	if err != nil {
		return "", fmt.Errorf("trust prompt for %s: %w", path, err)
	}
	if decision != TrustTrusted {
		decision = TrustUntrusted
	}
	if err := g.store.Record(TrustEntry{Path: abs, SHA256: hash, Decision: decision, DecidedAt: g.now().UTC()}); err != nil {
		return "", err
	}
	if decision == TrustTrusted {
		return "", nil
	}
	return RejectDeclined, nil
}

// isRiskFlag matches keys whose last segment is accept_*_risk: the opt-in
// flags, which only the user layer may set.
func isRiskFlag(key string) bool {
	parts := splitKey(key)
	last := parts[len(parts)-1]
	return strings.HasPrefix(last, "accept_") && strings.HasSuffix(last, "_risk")
}

// DroppedKeys groups every rejected repository key by reason, sorted, for
// the CLI warning. Nil when nothing was dropped.
func (r *Resolved) DroppedKeys() map[RejectReason][]string {
	var out map[RejectReason][]string
	for key, chain := range r.Provenance {
		for _, e := range chain {
			if !e.Rejected {
				continue
			}
			if out == nil {
				out = map[RejectReason][]string{}
			}
			out[e.Reason] = append(out[e.Reason], key)
		}
	}
	for _, keys := range out {
		sort.Strings(keys)
	}
	return out
}
