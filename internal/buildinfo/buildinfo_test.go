package buildinfo

import (
	"strings"
	"testing"
)

func TestSummary(t *testing.T) {
	if s := Summary(); !strings.HasPrefix(s, "friday dev (commit ") || !strings.HasSuffix(s, ")") {
		t.Fatalf("Summary() = %q", s)
	}
}
