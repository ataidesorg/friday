//go:build unix

package process

import (
	"os/exec"
	"syscall"
)

// setProcAttr puts the command in its own process group so a timeout kills
// the whole tree, not just the direct child.
//
// ponytail: rlimits (RLIMIT_AS, RLIMIT_NPROC, RLIMIT_FSIZE) are not applied;
// os/exec cannot set them on the child and prlimit is Linux-only. The
// container provider enforces memory, process, and disk limits.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// enforcedLimits names what this OS actually enforces.
func enforcedLimits() []string { return []string{"process_group_kill", "wall_clock", "output_bytes"} }
