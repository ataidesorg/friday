package workspace_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/workspace"
)

func TestTaskBranch(t *testing.T) {
	got, err := workspace.TaskBranch("fix-auth")
	if err != nil || got != "friday/fix-auth" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := workspace.TaskBranch("../etc"); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("escape: %v", err)
	}
	if _, err := workspace.TaskBranch("has space"); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("space: %v", err)
	}
}

func TestCommitStep(t *testing.T) {
	root := repo(t)
	if _, err := workspace.CommitStep(context.Background(), root, "  "); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("empty message: %v", err)
	}
	clean, err := workspace.CommitStep(context.Background(), root, "noop")
	if err != nil || clean.Created || clean.Hash == "" {
		t.Fatalf("clean %+v %v", clean, err)
	}
	write(t, root, "step.go", "package step\n")
	got, err := workspace.CommitStep(context.Background(), root, "feat: add step")
	if err != nil || !got.Created || got.Hash == clean.Hash {
		t.Fatalf("dirty %+v %v", got, err)
	}
	log := git(t, root, "log", "-1", "--format=%s")
	if strings.TrimSpace(log) != "feat: add step" {
		t.Fatalf("message %q", log)
	}
	if !strings.Contains(git(t, root, "show", "--name-only", "--pretty=format:", got.Hash), "step.go") {
		t.Fatal("commit missing step.go")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := workspace.CommitStep(cancelled, root, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}
