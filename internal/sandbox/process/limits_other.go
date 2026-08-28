//go:build !unix

package process

import "os/exec"

// setProcAttr is a no-op outside unix: only the direct child is killed on
// timeout.
func setProcAttr(*exec.Cmd) {}

// enforcedLimits names what this OS actually enforces.
func enforcedLimits() []string { return []string{"wall_clock", "output_bytes"} }
