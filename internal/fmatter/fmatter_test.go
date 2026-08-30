package fmatter

import (
	"errors"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
)

func TestSplit(t *testing.T) {
	meta, body, err := Split([]byte("---\ndescription = \"x\"\n---\nhello\nworld\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), "description") || body != "hello\nworld\n" {
		t.Fatalf("meta %q body %q", meta, body)
	}
}

func TestSplitNoFrontmatter(t *testing.T) {
	meta, body, err := Split([]byte("plain prompt\n"))
	if err != nil || meta != nil || body != "plain prompt\n" {
		t.Fatalf("meta %q body %q err %v", meta, body, err)
	}
}

func TestSplitUnclosed(t *testing.T) {
	_, _, err := Split([]byte("---\ndescription = \"x\"\nno close"))
	if !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("err %v", err)
	}
}

func TestSplitCRLF(t *testing.T) {
	meta, body, err := Split([]byte("---\r\na = 1\r\n---\r\nbody"))
	if err != nil || !strings.Contains(string(meta), "a = 1") || body != "body" {
		t.Fatalf("meta %q body %q err %v", meta, body, err)
	}
}
