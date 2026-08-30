package auth

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ataidesorg/ink/internal/config"
	"github.com/ataidesorg/ink/internal/core"
)

// resolveCommand runs the user-layer argv and treats trimmed stdout as the
// credential. Output is never logged: a failure reports only the exit state,
// because stderr and stdout may both carry the secret.
func (r *Resolver) resolveCommand(ctx context.Context, ref config.AuthRef) (*Credential, error) {
	if len(ref.Command) == 0 {
		return nil, fmt.Errorf("%w: auth source \"command\" needs a command argv", core.ErrInvalidInput)
	}
	out, err := r.exec(ctx, ref.Command, "")
	if err != nil {
		return nil, fmt.Errorf("auth command %q failed (output withheld: it may contain the credential)", ref.Command[0])
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return nil, &ErrNoCredential{Source: "command", Where: ref.Command[0] + " printed nothing"}
	}
	return r.credential(v), nil
}

// defaultExec runs argv, feeding stdin and capturing stdout. Stderr is
// discarded: it may carry the credential and must never reach logs.
func defaultExec(ctx context.Context, argv []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is the user-layer auth command; running it is the feature, and validation rejects it from repository layers
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd.Output()
}
