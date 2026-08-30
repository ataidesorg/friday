package runtime

import (
	"testing"

	"github.com/ataidesorg/ink/internal/core"
)

func TestMemoryTarget(t *testing.T) {
	ns, cat, sens := memoryTarget("sample")
	if ns != "project:sample" || cat != core.MemoryProject || sens != core.SensitivityInternal {
		t.Fatalf("named: %s %s %s", ns, cat, sens)
	}
	ns, cat, sens = memoryTarget("")
	if ns != "project" || cat != core.MemoryProject || sens != core.SensitivityInternal {
		t.Fatalf("empty: %s %s %s", ns, cat, sens)
	}
}
