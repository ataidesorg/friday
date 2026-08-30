package buildinfo

import (
	"strings"
	"testing"
)

func TestSummary(t *testing.T) {
	if s := Summary(); !strings.HasPrefix(s, "ink dev (commit ") || !strings.HasSuffix(s, ")") {
		t.Fatalf("Summary() = %q", s)
	}
}
