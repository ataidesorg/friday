package core

import (
	"fmt"
	"slices"
	"strings"
)

// Subtask is one node in a TaskGraph. ID is a caller-chosen label (not a
// UUID) so a plan can name worktrees after the nodes. Deps are other node
// IDs that must finish first. Worktree is the dedicated checkout name; an
// empty value means the caller will fill it in later.
type Subtask struct {
	ID       string   `json:"id"`
	Title    string   `json:"title,omitempty"`
	Deps     []string `json:"deps,omitempty"`
	Worktree string   `json:"worktree,omitempty"`
}

// TaskGraph is a directed acyclic graph of subtasks. Waves groups nodes
// that can run in parallel; a merge step consumes the finished worktrees.
type TaskGraph struct {
	Nodes []Subtask `json:"nodes"`
}

// Validate rejects an empty graph, blank or duplicate IDs, missing deps,
// self-edges, and cycles.
func (g TaskGraph) Validate() error {
	if len(g.Nodes) == 0 {
		return fmt.Errorf("%w: task graph is empty", ErrInvalidInput)
	}
	index := make(map[string]int, len(g.Nodes))
	for i, n := range g.Nodes {
		id := strings.TrimSpace(n.ID)
		if id == "" {
			return fmt.Errorf("%w: subtask %d has no id", ErrInvalidInput, i)
		}
		if _, dup := index[id]; dup {
			return fmt.Errorf("%w: duplicate subtask id %q", ErrInvalidInput, id)
		}
		index[id] = i
	}
	for _, n := range g.Nodes {
		seen := map[string]bool{}
		for _, dep := range n.Deps {
			if dep == n.ID {
				return fmt.Errorf("%w: subtask %q depends on itself", ErrInvalidInput, n.ID)
			}
			if _, ok := index[dep]; !ok {
				return fmt.Errorf("%w: subtask %q depends on unknown %q", ErrInvalidInput, n.ID, dep)
			}
			if seen[dep] {
				continue
			}
			seen[dep] = true
		}
	}
	if cycle := g.cycle(index); cycle != "" {
		return fmt.Errorf("%w: cycle in task graph (%s)", ErrInvalidInput, cycle)
	}
	return nil
}

// Waves returns successive parallel groups: every node in a wave has all of
// its deps in earlier waves. Nodes inside a wave are sorted by ID.
func (g TaskGraph) Waves() ([][]Subtask, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	remaining := make(map[string]Subtask, len(g.Nodes))
	indeg := make(map[string]int, len(g.Nodes))
	for _, n := range g.Nodes {
		remaining[n.ID] = n
		seen := map[string]bool{}
		for _, d := range n.Deps {
			if seen[d] {
				continue
			}
			seen[d] = true
			indeg[n.ID]++
		}
	}
	var waves [][]Subtask
	for len(remaining) > 0 {
		var wave []Subtask
		for id, n := range remaining {
			if indeg[id] == 0 {
				wave = append(wave, n)
			}
		}
		if len(wave) == 0 {
			return nil, fmt.Errorf("%w: cycle in task graph", ErrInvalidInput)
		}
		slices.SortFunc(wave, func(a, b Subtask) int { return strings.Compare(a.ID, b.ID) })
		waves = append(waves, wave)
		for _, n := range wave {
			delete(remaining, n.ID)
			for _, other := range remaining {
				for _, d := range other.Deps {
					if d == n.ID {
						indeg[other.ID]--
					}
				}
			}
		}
	}
	return waves, nil
}

func (g TaskGraph) cycle(index map[string]int) string {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make([]int, len(g.Nodes))
	var path []string
	var found []string
	var visit func(int) bool
	visit = func(i int) bool {
		color[i] = grey
		path = append(path, g.Nodes[i].ID)
		for _, dep := range g.Nodes[i].Deps {
			j := index[dep]
			switch color[j] {
			case white:
				if visit(j) {
					return true
				}
			case grey:
				found = append([]string(nil), path...)
				found = append(found, g.Nodes[j].ID)
				return true
			}
		}
		path = path[:len(path)-1]
		color[i] = black
		return false
	}
	for i := range g.Nodes {
		if color[i] == white && visit(i) {
			return strings.Join(found, " → ")
		}
	}
	return ""
}

// SplitBudget divides cost and tool-call caps across n parallel subtasks.
// Remainders go to the last share. Wall-clock is copied, not divided: waves
// run in parallel so each subtask keeps the original ceiling. n must be > 0.
func SplitBudget(b TaskBudget, n int) ([]TaskBudget, error) {
	if n <= 0 {
		return nil, fmt.Errorf("%w: budget split needs a positive share count", ErrInvalidInput)
	}
	out := make([]TaskBudget, n)
	cost, calls := b.MaxCost, int64(b.MaxToolCalls)
	for i := 0; i < n; i++ {
		out[i] = TaskBudget{MaxWallClock: b.MaxWallClock}
		left := int64(n - i)
		if cost != 0 {
			share := cost / USDMicros(left)
			out[i].MaxCost = share
			cost -= share
		}
		if calls != 0 {
			share := calls / left
			out[i].MaxToolCalls = int(share)
			calls -= share
		}
	}
	return out, nil
}
