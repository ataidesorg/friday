// Package cli is the ink command line: flag parsing, config loading, and
// the wiring that turns a subcommand into a runtime graph.
package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/ataidesorg/ink/internal/buildinfo"
	"github.com/ataidesorg/ink/internal/core"
)

const usageText = `usage: ink <command> [flags]

commands:
  version                      print version and commit
  config show                  print the effective configuration as TOML
  config validate              check the effective configuration; "ok" or one error per line
  config explain KEY           show which layer set KEY and every value it passed through
  run "task text"              run a task (ink run -h for flags; --script FILE drives the mock provider)
  trace RUN_ID                 replay a run's event trail (--json for raw lines, --kind K,... to filter)
  eval run|bench SCENARIO.json run a scenario; bench also judges the cheap/fast bar (--script FILE)
  providers                    list model providers; --check probes the ones whose credentials resolve
  auth set PROVIDER            store a credential in the encrypted secret store (prompted, never argv)
  auth status                  show which providers have credentials; never the values
  model [--set ROUTE]          show or set the default model route (interactive on a terminal)
  models --provider ID         list the provider's advertised models (--refresh bypasses the 24h cache)
  init                         create .ink/config.toml and git-ignore Ink's local files
  trust [PATH]                 trust a repository config file at its current content (--list, --revoke)
  chat                         open the interactive chat REPL (the default when run on a terminal)
  sessions                     list saved chat sessions, newest first

config flags:
  --project DIR    project root holding .ink/config.toml (default: current directory)
  --profile NAME   profile to activate (default: profile.active)
  --config-dir DIR user config directory (default: $INK_CONFIG_DIR, $XDG_CONFIG_HOME/ink, ~/.config/ink)
  --set key=value  override one key; repeatable

exit codes: 0 ok, 1 error, 2 usage; ink run adds 0 verified, 1 unverified, 3 failed,
  4 escalated, 5 rolled back, 6 not implemented, 7 policy denied, 8 invalid configuration
`

// Main is the process entrypoint: it loads the user's env file and runs the
// CLI against the real streams. cmd/ink is a two-line shim over it.
func Main() int {
	loadUserEnv()
	return Run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin, os.Environ(), os.Getwd)
}

// Run is the whole CLI with every side channel injected so tests drive it.
func Run(args []string, stdout, stderr io.Writer, stdin io.Reader, environ []string, getwd func() (string, error)) int {
	if len(args) == 0 {
		if isTerminal(stdin) && isTerminal(stdout) {
			return chatCmd(nil, stdout, stderr, stdin, environ, getwd)
		}
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return exitOK
	case "version":
		fmt.Fprintln(stdout, buildinfo.Summary())
		return exitOK
	case "config":
		return configCmd(args[1:], stdout, stderr, environ, getwd)
	case "run":
		return runCmd(args[1:], stdout, stderr, stdin, environ, getwd)
	case "eval":
		return evalCmd(args[1:], stdout, stderr, environ, getwd)
	case "providers":
		return providersCmd(args[1:], stdout, stderr, environ, getwd)
	case "auth":
		return authCmd(args[1:], stdout, stderr, stdin, environ, getwd)
	case "model":
		return modelCmd(args[1:], stdout, stderr, stdin, environ, getwd)
	case "models":
		return modelsCmd(args[1:], stdout, stderr, environ, getwd)
	case "trace":
		return traceCmd(args[1:], stdout, stderr, getwd)
	case "init":
		return initCmd(args[1:], stdout, stderr, getwd)
	case "trust":
		return trustCmd(args[1:], stdout, stderr, environ, getwd)
	case "chat":
		return chatCmd(args[1:], stdout, stderr, stdin, environ, getwd)
	case "sessions":
		return sessionsCmd(args[1:], stdout, stderr, environ)
	case "--resume", "--continue":
		return chatCmd(args, stdout, stderr, stdin, environ, getwd)
	default:
		fmt.Fprintf(stderr, "ink: unknown command %q\n\n%s", args[0], usageText)
		return exitUsage
	}
}

// fail prints "ink <cmd>: <err>" and returns code, so every command's
// error paths stay one line each.
func fail(w io.Writer, cmd string, code int, err error) int {
	fmt.Fprintf(w, "ink %s: %v\n", cmd, err)
	return code
}

// Exit codes. `ink run` maps the outcome (exitFor); other commands use
// exitOK, exitError, and exitUsage.
const (
	exitOK             = 0 // completed_verified, or success for other commands
	exitError          = 1 // generic failure for non-run commands
	exitUnverified     = 1 // completed_unverified
	exitUsage          = 2
	exitFailed         = 3 // failed (any category but policy_denied)
	exitEscalated      = 4
	exitRolledBack     = 5
	exitNotImplemented = 6
	exitPolicyDenied   = 7 // failed with category policy_denied
	exitConfigInvalid  = 8
)

// exitFor maps a run outcome to the process exit code; unknown kinds are
// failures so a caller never mistakes them for success.
func exitFor(o core.Outcome) int {
	switch o.Kind {
	case core.OutcomeCompletedVerified:
		return exitOK
	case core.OutcomeCompletedUnverified:
		return exitUnverified
	case core.OutcomeEscalated:
		return exitEscalated
	case core.OutcomeRolledBack:
		return exitRolledBack
	case core.OutcomeFailed:
		if o.Category == core.FailurePolicyDenied {
			return exitPolicyDenied
		}
		return exitFailed
	default:
		return exitFailed
	}
}
