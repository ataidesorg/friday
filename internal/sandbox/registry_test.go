package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/core"
)

type stubProvider struct{ o Options }

func (stubProvider) Name() string { return "stub" }
func (stubProvider) Create(context.Context, core.SandboxSpec) (core.Sandbox, error) {
	return nil, core.ErrUnavailable
}

type stubSandbox struct{ core.Sandbox }

func (stubSandbox) ReadFile(context.Context, string) ([]byte, error) { return nil, nil }
func (stubSandbox) WriteFile(context.Context, string, []byte, fs.FileMode) error {
	return nil
}

func TestOptionsDefaults(t *testing.T) {
	o := Options{}.WithDefaults()
	if o.Redactor == nil || o.Clock == nil || o.MaxOutputBytes != DefaultMaxOutputBytes || o.KeepWorkspace {
		t.Fatalf("defaults not filled: %+v", o)
	}
	fixed := func() time.Time { return time.Unix(1, 0) }
	kept := Options{MaxOutputBytes: 7, Clock: fixed, KeepWorkspace: true}.WithDefaults()
	if kept.MaxOutputBytes != 7 || !kept.KeepWorkspace || !kept.Clock().Equal(time.Unix(1, 0)) {
		t.Fatalf("explicit options overwritten: %+v", kept)
	}
}

func TestRegistry(t *testing.T) {
	r := Registry{"stub": func(o Options) core.SandboxProvider { return stubProvider{o: o} }, "nil": nil}
	p, err := r.New("stub", Options{})
	if err != nil || p.Name() != "stub" {
		t.Fatalf("New(stub) = %v, %v", p, err)
	}
	for _, name := range []string{"missing", "nil"} {
		if _, err := r.New(name, Options{}); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("New(%s): want ErrNotFound, got %v", name, err)
		}
	}
}

func TestCapabilityProbes(t *testing.T) {
	if _, ok := Files(core.Sandbox(nil)); ok {
		t.Error("nil sandbox must not report file access")
	}
	if _, ok := Snapshots(stubSandbox{}); ok {
		t.Error("stub must not report snapshots")
	}
	if _, ok := Files(stubSandbox{}); !ok {
		t.Error("stub implements FileAccess")
	}
}
