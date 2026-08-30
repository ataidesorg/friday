package config

import "maps"

// Layer is one configuration source in precedence order.
type Layer string

// Layers, lowest to highest precedence.
const (
	LayerDefaults     Layer = "defaults"
	LayerUser         Layer = "user"
	LayerProfile      Layer = "profile"
	LayerProject      Layer = "project"
	LayerProjectLocal Layer = "project_local"
	LayerEnv          Layer = "env"
	LayerCLI          Layer = "cli"
)

// LayerOrder is the merge order; later layers win.
var LayerOrder = []Layer{LayerDefaults, LayerUser, LayerProfile, LayerProject, LayerProjectLocal, LayerEnv, LayerCLI}

// RejectReason says why a repository layer's value was recorded but not merged.
type RejectReason string

// Reject reasons. RejectAllowlist is permanent (opt-in risk flags are user
// layer only); the others clear once the owner trusts the file at its
// current content. RejectUntrusted covers "no decision on record" whether or
// not a prompt could have fired: the remedy (`ink trust`) is the same.
// RejectDeclined means the owner said no to this exact content; editing the
// file clears it, because the answer no longer describes what is on disk.
const (
	RejectAllowlist   RejectReason = "allowlist"
	RejectUntrusted   RejectReason = "untrusted"
	RejectHashChanged RejectReason = "hash_changed"
	RejectDeclined    RejectReason = "declined"
)

// Source is where a value came from. Path is empty for defaults, env, and cli.
type Source struct {
	Layer Layer
	Path  string
}

// Entry is one value a layer set for a key. Rejected entries were recorded
// but never merged; Reason says why (see RejectReason).
type Entry struct {
	Source   Source
	Value    any
	Rejected bool
	Reason   RejectReason
}

// Provenance maps a dotted key to every layer that set it, ascending.
type Provenance map[string][]Entry

// merge returns a new map with src merged over dst: tables recurse, arrays
// and scalars replace. Neither input is modified; prov accumulates one
// Entry per leaf that src set.
func merge(dst, src map[string]any, from Source, prov Provenance, prefix string) map[string]any {
	out := make(map[string]any, len(dst)+len(src))
	maps.Copy(out, dst)
	for k, v := range src {
		key := prefix + k
		srcTable, srcIsTable := v.(map[string]any)
		if !srcIsTable {
			out[k] = v
			prov[key] = append(prov[key], Entry{Source: from, Value: v})
			continue
		}
		dstTable, _ := out[k].(map[string]any)
		out[k] = merge(dstTable, srcTable, from, prov, key+".")
	}
	return out
}

// lookup returns the value at a dotted key in a nested map.
func lookup(m map[string]any, key string) (any, bool) {
	parts := splitKey(key)
	var cur any = m
	for _, p := range parts {
		table, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = table[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// setNested returns a copy of m with value stored at the dotted key path,
// creating tables as needed. A scalar in the way is an error.
func setNested(m map[string]any, parts []string, value any) (map[string]any, error) {
	out := make(map[string]any, len(m)+1)
	maps.Copy(out, m)
	if len(parts) == 1 {
		if _, isTable := out[parts[0]].(map[string]any); isTable {
			return nil, errKeyConflict(parts[0])
		}
		out[parts[0]] = value
		return out, nil
	}
	child, _ := out[parts[0]].(map[string]any)
	if child == nil {
		if _, exists := out[parts[0]]; exists {
			return nil, errKeyConflict(parts[0])
		}
		child = map[string]any{}
	}
	sub, err := setNested(child, parts[1:], value)
	if err != nil {
		return nil, err
	}
	out[parts[0]] = sub
	return out, nil
}

// flatten lists every leaf under m as dotted key → value. Empty tables
// produce nothing.
func flatten(m map[string]any, prefix string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if table, ok := v.(map[string]any); ok {
			maps.Copy(out, flatten(table, prefix+k+"."))
			continue
		}
		out[prefix+k] = v
	}
	return out
}
