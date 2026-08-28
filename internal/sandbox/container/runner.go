package container

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

// Result is one invocation of the container CLI.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner is the injectable container CLI. Tests pass a fake; production
// uses CLIRunner("docker") or CLIRunner("podman"). argv is the arguments
// after the binary (run, exec, rm, commit).
type Runner func(ctx context.Context, argv []string, stdin string) (Result, error)

// CLIRunner shells out to bin with no shell.
func CLIRunner(bin string) Runner {
	return func(ctx context.Context, argv []string, stdin string) (Result, error) {
		cmd := exec.CommandContext(ctx, bin, argv...) //nolint:gosec // bin is docker/podman from Settings; argv is built here, never from the model
		cmd.Stdin = strings.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
		if err == nil {
			return res, nil
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			res.ExitCode = exit.ExitCode()
			return res, nil
		}
		return res, err
	}
}

func detectRuntime() string {
	if _, err := exec.LookPath("docker"); err == nil {
		return "docker"
	}
	if _, err := exec.LookPath("podman"); err == nil {
		return "podman"
	}
	return "docker"
}
