package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"

	"github.com/ataidesorg/friday/internal/buildinfo"
	"github.com/ataidesorg/friday/internal/config"
	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/evals"
	"github.com/ataidesorg/friday/internal/models/mock"
	"github.com/ataidesorg/friday/internal/observability"
	"github.com/ataidesorg/friday/internal/redact"
)

const evalUsage = `usage: friday eval run|bench SCENARIO.json --script FILE [flags]

Runs the scenario's task on a private copy of its fixture (the fixture is the
project root; its .friday/config.toml applies) and checks every expectation.
Writes and allow-listed commands are pre-approved; nothing else is.

  run    print per-check results; exit 0 only if every expectation passed
  bench  same run, then judge elapsed/tokens/cost against the mock harness bar

flags:
  --script FILE      scripted mock provider (the only provider eval accepts)
  --model NAME       model name (default: the script's model)
  --profile NAME     profile to activate
  --config-dir DIR   user config directory
  --set key=value    override one key; repeatable

exit codes: 0 passed, 1 failed, 2 usage, 3 run error, 6 expectation kind not
implemented yet, 8 invalid configuration
`

type evalFlags struct {
	globalFlags
	script, model string
}

func parseEvalFlags(args []string, stderr io.Writer) (evalFlags, string, bool) {
	var f evalFlags
	fs := flag.NewFlagSet("eval run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f.bind(fs)
	fs.StringVar(&f.script, "script", "", "scripted mock provider file")
	fs.StringVar(&f.model, "model", "", "model name")
	positional, err := parseInterleaved(fs, args)
	if err != nil || len(positional) != 1 {
		fmt.Fprint(stderr, evalUsage)
		return f, "", false
	}
	return f, positional[0], true
}

// evalCmd runs one scenario: scenario → config (fixture as project) → graph → evals.Runner.
func evalCmd(args []string, stdout, stderr io.Writer, environ []string, getwd func() (string, error)) int {
	if len(args) == 0 || (args[0] != "run" && args[0] != "bench") {
		fmt.Fprint(stderr, evalUsage)
		return exitUsage
	}
	mode := args[0]
	f, path, ok := parseEvalFlags(args[1:], stderr)
	if !ok {
		return exitUsage
	}
	scenario, err := evals.LoadScenario(path)
	if err != nil {
		return fail(stderr, "eval", exitUsage, err)
	}
	f.project = scenario.Fixture
	opts, err := f.options(environ, getwd, stderr)
	if err != nil {
		return fail(stderr, "eval", exitUsage, err)
	}
	resolved, err := config.Load(opts)
	if err != nil {
		return fail(stderr, "eval", exitConfigInvalid, err)
	}
	warnDropped(stderr, resolved)
	red := redact.New()
	if verr := config.Validate(resolved); verr != nil {
		fmt.Fprintf(stderr, "friday eval: configuration is invalid\n%s\n", red.Redact(verr.Error()))
		return exitConfigInvalid
	}
	cfg := resolved.Config
	if f.script == "" {
		fmt.Fprintln(stderr, "friday eval: no provider configured; pass --script FILE to run a scripted model")
		return exitFailed
	}
	script, err := mock.LoadScript(f.script)
	if err != nil {
		return fail(stderr, "eval", exitFailed, err)
	}
	if cfg.Sandbox.Provider == "unavailable" {
		fmt.Fprintln(stderr, "friday eval: sandbox.provider is \"unavailable\"; set it to \"process\" to run scenarios")
		return exitNotImplemented
	}
	model := f.model
	if model == "" {
		model = script.Model
	}
	deps, in, err := buildGraph(cfg, mock.New(script), model, red, false)
	if err != nil {
		return fail(stderr, "eval", exitFailed, err)
	}
	deps.Approve = yesApprover(nil)
	runner := &evals.Runner{Deps: deps, Input: in, Redactor: red, Privacy: core.PrivacyMode(cfg.Telemetry.Privacy), Version: buildinfo.Version, Commit: buildinfo.Commit}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := runner.Run(ctx, scenario)
	switch {
	case errors.Is(err, core.ErrNotImplemented):
		return fail(stderr, "eval", exitNotImplemented, err)
	case err != nil:
		fmt.Fprintf(stderr, "friday eval: %v\n", red.Redact(err.Error()))
		return exitFailed
	}
	printEvaluation(stdout, res)
	if mode == "bench" {
		v := evals.Judge(res, evals.MockBar())
		fmt.Fprint(stdout, evals.FormatVerdict(v, res))
		if !v.Met {
			return exitError
		}
		return exitOK
	}
	if !res.Passed {
		return exitError
	}
	return exitOK
}

func printEvaluation(w io.Writer, res core.EvaluationResult) {
	verdict := "passed"
	if !res.Passed {
		verdict = "failed"
	}
	fmt.Fprintf(w, "scenario %s: %s (%d checks, %s, run %s)\n", res.Scenario, verdict, len(res.Checks), res.Elapsed.Round(1e6), res.Run)
	for _, c := range res.Checks {
		mark := "pass"
		if !c.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(w, "  %s %s — %s\n", mark, expectationLabel(c.Expectation), c.Detail)
	}
	if res.Failure != "" {
		fmt.Fprintf(w, "  run failure: %s\n", res.Failure)
	}
}

func expectationLabel(e core.Expectation) string {
	parts := []string{string(e.Kind)}
	switch e.Kind {
	case core.ExpectFileExists:
		parts = append(parts, e.Path)
	case core.ExpectFileContains:
		parts = append(parts, e.Path, fmt.Sprintf("%q", e.Needle))
	case core.ExpectFileSHA256:
		parts = append(parts, e.Path, e.SHA256)
	case core.ExpectCommandSucceeds, core.ExpectCommandFails:
		parts = append(parts, strings.Join(e.Argv, " "))
	case core.ExpectApprovalRequired:
		parts = append(parts, string(e.Risk))
	case core.ExpectMemoryWritten:
		parts = append(parts, e.Memory)
	}
	return strings.Join(parts, " ")
}

// traceCmd replays a run's local event trail.
func traceCmd(args []string, stdout, stderr io.Writer, getwd func() (string, error)) int {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project root")
	asJSON := fs.Bool("json", false, "print raw event lines")
	kinds := fs.String("kind", "", "comma-separated event kinds to keep")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: friday trace [--project DIR] [--json] [--kind K,...] RUN_ID")
		return exitUsage
	}
	id := fs.Arg(0)
	if !core.ValidID(id) {
		fmt.Fprintf(stderr, "friday: run not found: %q is not a run id\n", id)
		return exitUsage
	}
	root := *project
	if root == "" {
		wd, err := getwd()
		if err != nil {
			fmt.Fprintf(stderr, "friday: %v\n", err)
			return exitError
		}
		root = wd
	}
	events, err := observability.ReadTrail(observability.TrailPath(root, core.RunID(id)))
	switch {
	case errors.Is(err, core.ErrNotFound):
		fmt.Fprintf(stderr, "friday: run not found: %s\n", id)
		return exitUsage
	case err != nil:
		fmt.Fprintf(stderr, "friday: %v\n", err)
		return exitError
	}
	var opts observability.TraceOptions
	opts.JSON = *asJSON
	for _, k := range strings.Split(*kinds, ",") {
		if k = strings.TrimSpace(k); k != "" {
			opts.Kinds = append(opts.Kinds, core.EventKind(k))
		}
	}
	if err := observability.Trace(stdout, events, opts); err != nil {
		fmt.Fprintf(stderr, "friday: %v\n", err)
		if errors.Is(err, core.ErrInvalidInput) {
			return exitUsage
		}
		return exitError
	}
	return exitOK
}

func doctorReport() []string {
	env := func(k string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return "(unset)"
	}
	tmux := "off"
	if os.Getenv("TMUX") != "" {
		tmux = "on"
	}
	ssh := "off"
	if os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != "" {
		ssh = "on"
	}
	return []string{
		"go " + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH,
		"term " + env("TERM"),
		"term_program " + env("TERM_PROGRAM"),
		"colorterm " + env("COLORTERM"),
		"tmux " + tmux,
		"ssh " + ssh,
	}
}
