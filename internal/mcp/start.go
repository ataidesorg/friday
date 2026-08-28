package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/ataidesorg/friday/internal/core"
)

// Start launches a config-declared server process and wires a client to its
// stdio. Production only — tests drive NewClient over in-process pipes and
// never spawn processes.
func Start(ctx context.Context, name string, argv []string) (*Client, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, fmt.Errorf("%w: mcp server %s has no command", core.ErrInvalidInput, name)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv comes from user-layer config the owner wrote
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %s: %w", name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %s: %w", name, err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp %s: %w", name, err)
	}
	c := NewClient(name, stdout, stdin)
	c.stop = func() error {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		err := cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil // we killed it; a non-zero exit is the expected shape
		}
		return err
	}
	return c, nil
}
