package runtime

import (
	"testing"

	"github.com/ataidesorg/friday/internal/core"
)

func TestMemoryTarget(t *testing.T) {
	ns, cat, sens := memoryTarget(core.AgentProfile{}, "sample")
	if ns != "project:sample" || cat != core.MemoryProject || sens != core.SensitivityInternal {
		t.Fatalf("code zero: %s %s %s", ns, cat, sens)
	}
	ns, cat, sens = memoryTarget(core.DefaultAssistantProfile(), "sample")
	if ns != "personal" || cat != core.MemoryProject || sens != core.SensitivityPersonal {
		t.Fatalf("assistant: %s %s %s", ns, cat, sens)
	}
	capped := core.DefaultAssistantProfile()
	capped.SensitivityCap = core.SensitivityInternal
	_, _, sens = memoryTarget(capped, "")
	if sens != core.SensitivityInternal {
		t.Fatalf("cap lowered to %s", sens)
	}
}
