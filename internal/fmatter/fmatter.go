// Package fmatter splits an optional TOML frontmatter block from a Markdown
// file: a leading "---" line, TOML until the next "---" line, then the body.
package fmatter

import (
	"fmt"
	"strings"

	"github.com/ataidesorg/ink/internal/core"
)

const marker = "---"

// Split returns the TOML between the frontmatter markers (nil when the file
// has none) and the body after it. An unclosed block is an error, never a
// silent misparse.
func Split(b []byte) (meta []byte, body string, err error) {
	s := string(b)
	first, rest, found := strings.Cut(s, "\n")
	if !found || strings.TrimRight(first, "\r") != marker {
		return nil, s, nil
	}
	for cut := 0; ; {
		nl := strings.Index(rest[cut:], "\n")
		if nl < 0 {
			if strings.TrimRight(rest[cut:], "\r") == marker {
				return []byte(rest[:cut]), "", nil
			}
			return nil, "", fmt.Errorf("%w: frontmatter never closed with %q", core.ErrInvalidInput, marker)
		}
		line := rest[cut : cut+nl]
		if strings.TrimRight(line, "\r") == marker {
			return []byte(rest[:cut]), rest[cut+nl+1:], nil
		}
		cut += nl + 1
	}
}
