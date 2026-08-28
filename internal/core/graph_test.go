package core

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTaskGraphValidateAndWaves(t *testing.T) {
	g := TaskGraph{Nodes: []Subtask{
		{ID: "api", Title: "API"},
		{ID: "ui", Title: "UI", Deps: []string{"api"}},
		{ID: "docs", Title: "Docs"},
	}}
	waves, err := g.Waves()
	if err != nil {
		t.Fatal(err)
	}
	if len(waves) != 2 {
		t.Fatalf("waves = %d, want 2: %+v", len(waves), waves)
	}
	if ids(waves[0]) != "api,docs" {
		t.Errorf("wave 0 = %s, want api,docs (parallel roots, sorted)", ids(waves[0]))
	}
	if ids(waves[1]) != "ui" {
		t.Errorf("wave 1 = %s", ids(waves[1]))
	}
}

func TestTaskGraphRejects(t *testing.T) {
	cases := []struct {
		name string
		g    TaskGraph
	}{
		{"empty", TaskGraph{}},
		{"blank id", TaskGraph{Nodes: []Subtask{{ID: " ", Title: "x"}}}},
		{"dup", TaskGraph{Nodes: []Subtask{{ID: "a"}, {ID: "a"}}}},
		{"missing dep", TaskGraph{Nodes: []Subtask{{ID: "a", Deps: []string{"nope"}}}}},
		{"self", TaskGraph{Nodes: []Subtask{{ID: "a", Deps: []string{"a"}}}}},
		{"cycle", TaskGraph{Nodes: []Subtask{
			{ID: "a", Deps: []string{"b"}},
			{ID: "b", Deps: []string{"a"}},
		}}},
	}
	for _, c := range cases {
		if err := c.g.Validate(); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: want ErrInvalidInput, got %v", c.name, err)
		}
		if _, err := c.g.Waves(); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s Waves: want ErrInvalidInput, got %v", c.name, err)
		}
	}
}

func TestSplitBudget(t *testing.T) {
	b := TaskBudget{MaxCost: 10, MaxWallClock: time.Minute, MaxToolCalls: 10}
	got, err := SplitBudget(b, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []TaskBudget{
		{MaxCost: 3, MaxWallClock: time.Minute, MaxToolCalls: 3},
		{MaxCost: 3, MaxWallClock: time.Minute, MaxToolCalls: 3},
		{MaxCost: 4, MaxWallClock: time.Minute, MaxToolCalls: 4},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if _, err := SplitBudget(b, 0); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("n=0: %v", err)
	}
	zero, err := SplitBudget(TaskBudget{}, 2)
	if err != nil || len(zero) != 2 || zero[0] != (TaskBudget{}) || zero[1] != (TaskBudget{}) {
		t.Errorf("zero budget: %+v %v", zero, err)
	}
}

func ids(nodes []Subtask) string {
	var b strings.Builder
	for i, n := range nodes {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n.ID)
	}
	return b.String()
}
