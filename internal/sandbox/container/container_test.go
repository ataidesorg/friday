package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/redact"
	"github.com/ataidesorg/friday/internal/sandbox"
)

// secretish is assembled from fragments so the repo never holds a secret-shaped literal.
var secretish = "ghp_" + strings.Repeat("a1B2", 9)

type fakeCLI struct {
	mu    sync.Mutex
	calls [][]string
	stdin []string
	hook  func(ctx context.Context, argv []string, stdin string) (Result, error)
}

func (f *fakeCLI) runner() Runner {
	return func(ctx context.Context, argv []string, stdin string) (Result, error) {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		f.mu.Lock()
		f.calls = append(f.calls, append([]string(nil), argv...))
		f.stdin = append(f.stdin, stdin)
		f.mu.Unlock()
		if f.hook != nil {
			return f.hook(ctx, argv, stdin)
		}
		if len(argv) > 0 && argv[0] == "run" {
			return Result{Stdout: "cid123\n"}, nil
		}
		if len(argv) > 0 && argv[0] == "commit" {
			return Result{Stdout: "sha256:deadbeef\n"}, nil
		}
		return Result{}, nil
	}
}

func (f *fakeCLI) last(verb string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if len(f.calls[i]) > 0 && f.calls[i][0] == verb {
			return f.calls[i]
		}
	}
	return nil
}

func (f *fakeCLI) all(verb string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == verb {
			out = append(out, c)
		}
	}
	return out
}

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n")
	write("sub/notes.txt", "notes\n")
	write("debug.log", "log\n")
	return root
}

func provider(t *testing.T, cli *fakeCLI, o sandbox.Options) *Provider {
	t.Helper()
	return NewWith(o, Settings{Runtime: "docker", Image: DefaultImage, Run: cli.runner()})
}

func create(t *testing.T, cli *fakeCLI, o sandbox.Options, mod func(*core.SandboxSpec)) *Sandbox {
	t.Helper()
	spec := core.NewSandboxSpec(fixture(t))
	if mod != nil {
		mod(&spec)
	}
	sb, err := provider(t, cli, o).Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Destroy(context.Background()) })
	got, ok := sb.(*Sandbox)
	if !ok {
		t.Fatalf("Create returned %T", sb)
	}
	return got
}

func hasPair(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

func volume(argv []string, guest string) string {
	suffix := ":" + guest
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] != "-v" {
			continue
		}
		v := argv[i+1]
		if strings.HasSuffix(v, suffix) || strings.Contains(v, suffix+":") {
			return v
		}
	}
	return ""
}

func TestNameAndFactory(t *testing.T) {
	p := New(sandbox.Options{})
	if p.Name() != Name || Name != "container" {
		t.Fatalf("Name = %q", p.Name())
	}
	got := Factory(sandbox.Options{})
	if got.Name() != Name {
		t.Fatalf("Factory.Name = %q", got.Name())
	}
}

func TestCreateUsesNetworkNoneAndLimits(t *testing.T) {
	cli := &fakeCLI{}
	sb := create(t, cli, sandbox.Options{}, nil)
	info := sb.Info()
	if info.Provider != Name || info.ID == "" {
		t.Fatalf("info = %+v", info)
	}
	run := cli.last("run")
	if run == nil {
		t.Fatal("docker run was not invoked")
	}
	if !hasPair(run, "--network", "none") {
		t.Fatalf("create must pass --network none, got %v", run)
	}
	if !hasPair(run, "--memory", "2048m") || !hasPair(run, "--cpus", "1") || !hasPair(run, "--pids-limit", "64") {
		t.Fatalf("limits not mapped: %v", run)
	}
	if !hasPair(run, "-w", guestRoot) {
		t.Fatalf("workdir: %v", run)
	}
	if !hasPair(run, DefaultImage, "sleep") || run[len(run)-1] != "infinity" {
		t.Fatalf("keep-alive: %v", run)
	}
	vol := volume(run, guestRoot)
	if vol == "" {
		t.Fatalf("workspace volume missing: %v", run)
	}
	host := strings.TrimSuffix(strings.TrimSuffix(vol, ":ro"), ":"+guestRoot)
	if host == info.Spec.WorkDir {
		t.Fatal("copy mode must not bind the caller's tree")
	}
	if _, err := os.Stat(filepath.Join(host, "main.go")); err != nil {
		t.Errorf("copy missing main.go: %v", err)
	}
	if sb.cid != "cid123" {
		t.Errorf("cid = %q", sb.cid)
	}
}

func TestCreateInPlaceBindsWorkDir(t *testing.T) {
	cli := &fakeCLI{}
	sb := create(t, cli, sandbox.Options{}, func(s *core.SandboxSpec) { s.Source.Kind = core.SourceInPlace })
	run := cli.last("run")
	vol := volume(run, guestRoot)
	host := strings.TrimSuffix(strings.TrimSuffix(vol, ":ro"), ":"+guestRoot)
	want, err := filepath.EvalSymlinks(sb.Info().Spec.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(host)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("in_place volume host %s, want %s", got, want)
	}
}

func TestCreateHonoursExcludeAndMounts(t *testing.T) {
	cli := &fakeCLI{}
	extra := t.TempDir()
	if err := os.WriteFile(filepath.Join(extra, "secret.txt"), []byte("n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := create(t, cli, sandbox.Options{}, func(s *core.SandboxSpec) {
		s.Source.Exclude = []string{"*.log"}
		s.Mounts = []core.Mount{{Host: extra, Guest: "/mnt/extra", Writable: false}}
	})
	run := cli.last("run")
	host := strings.TrimSuffix(strings.TrimSuffix(volume(run, guestRoot), ":ro"), ":"+guestRoot)
	if _, err := os.Stat(filepath.Join(host, "debug.log")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("excluded log copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(host, "main.go")); err != nil {
		t.Errorf("unexcluded file missing: %v", err)
	}
	if !hasPair(run, "-v", extra+":/mnt/extra:ro") {
		t.Fatalf("read-only mount missing: %v", run)
	}
	_ = sb
}

func TestCreateFailsClosed(t *testing.T) {
	cli := &fakeCLI{}
	p := provider(t, cli, sandbox.Options{})
	ctx := context.Background()
	cases := []struct {
		name string
		mod  func(*core.SandboxSpec)
		want error
	}{
		{"secret env", func(s *core.SandboxSpec) { s.Env = map[string]string{"TOKEN": secretish} }, core.ErrSecretContent},
		{"relative workdir", func(s *core.SandboxSpec) { s.WorkDir = "rel" }, core.ErrInvalidInput},
		{"missing workdir", func(s *core.SandboxSpec) { s.WorkDir = filepath.Join(s.WorkDir, "nope") }, core.ErrNotFound},
	}
	for _, c := range cases {
		spec := core.NewSandboxSpec(fixture(t))
		c.mod(&spec)
		if _, err := p.Create(ctx, spec); !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", c.name, c.want, err)
		}
	}
	if len(cli.all("run")) != 0 {
		t.Errorf("refused specs must not start a container: %v", cli.all("run"))
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.Create(cancelled, core.NewSandboxSpec(fixture(t))); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled create: %v", err)
	}
}

func TestCreateRuntimeMissing(t *testing.T) {
	cli := &fakeCLI{hook: func(context.Context, []string, string) (Result, error) {
		return Result{}, errors.New("executable file not found")
	}}
	_, err := provider(t, cli, sandbox.Options{}).Create(context.Background(), core.NewSandboxSpec(fixture(t)))
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestCreateNonZeroRun(t *testing.T) {
	cli := &fakeCLI{hook: func(_ context.Context, argv []string, _ string) (Result, error) {
		if len(argv) > 0 && argv[0] == "run" {
			return Result{ExitCode: 125, Stderr: "unknown flag"}, nil
		}
		return Result{}, nil
	}}
	_, err := provider(t, cli, sandbox.Options{}).Create(context.Background(), core.NewSandboxSpec(fixture(t)))
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestCreateScrubsEnv(t *testing.T) {
	cli := &fakeCLI{}
	_ = create(t, cli, sandbox.Options{}, func(s *core.SandboxSpec) {
		s.Env = map[string]string{"FOO": "bar", "HTTP_PROXY": "http://proxy.example:3128", "https_proxy": "x"}
	})
	run := cli.last("run")
	if !hasPair(run, "-e", "FOO=bar") {
		t.Fatalf("missing -e FOO=bar: %v", run)
	}
	for _, leak := range []string{"HTTP_PROXY=", "https_proxy="} {
		for i := 0; i+1 < len(run); i++ {
			if run[i] == "-e" && strings.Contains(run[i+1], leak) {
				t.Errorf("leaked %s in %v", leak, run)
			}
		}
	}
}

func TestExecDelegatesToRuntime(t *testing.T) {
	cli := &fakeCLI{hook: func(_ context.Context, argv []string, _ string) (Result, error) {
		if argv[0] == "run" {
			return Result{Stdout: "cid123\n"}, nil
		}
		if argv[0] == "exec" {
			return Result{Stdout: "out\n", Stderr: "err\n", ExitCode: 3}, nil
		}
		return Result{}, nil
	}}
	sb := create(t, cli, sandbox.Options{}, nil)
	res, err := sb.Exec(context.Background(), core.ExecRequest{Argv: []string{"sh", "-c", "echo"}, Dir: "sub", Stdin: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || res.Stdout != "out\n" || res.Stderr != "err\n" || res.Elapsed <= 0 {
		t.Fatalf("result %+v", res)
	}
	execArgv := cli.last("exec")
	if !hasPair(execArgv, "-w", guestRoot+"/sub") || !contains(execArgv, "-i") || !contains(execArgv, "cid123") {
		t.Fatalf("exec argv %v", execArgv)
	}
	if got := execArgv[len(execArgv)-3:]; strings.Join(got, " ") != "sh -c echo" {
		t.Fatalf("command = %v", execArgv)
	}
	cli.mu.Lock()
	defer cli.mu.Unlock()
	if cli.stdin[len(cli.stdin)-1] != "hi" {
		t.Errorf("stdin = %q", cli.stdin[len(cli.stdin)-1])
	}
}

func TestExecRejectsBadInputAndEscape(t *testing.T) {
	cli := &fakeCLI{}
	sb := create(t, cli, sandbox.Options{}, nil)
	ctx := context.Background()
	for _, c := range []struct {
		req  core.ExecRequest
		want error
	}{
		{core.ExecRequest{}, core.ErrInvalidInput},
		{core.ExecRequest{Argv: []string{"true"}, Dir: "../outside"}, core.ErrInvalidInput},
		{core.ExecRequest{Argv: []string{"true"}, Dir: "/etc"}, core.ErrInvalidInput},
		{core.ExecRequest{Argv: []string{"true"}, Timeout: -1}, core.ErrInvalidInput},
	} {
		if _, err := sb.Exec(ctx, c.req); !errors.Is(err, c.want) {
			t.Errorf("%+v: want %v, got %v", c.req, c.want, err)
		}
	}
	if n := len(cli.all("exec")); n != 0 {
		t.Errorf("rejected execs still reached the runtime (%d)", n)
	}
}

func TestExecTimeout(t *testing.T) {
	cli := &fakeCLI{hook: func(ctx context.Context, argv []string, _ string) (Result, error) {
		if argv[0] == "run" {
			return Result{Stdout: "cid123\n"}, nil
		}
		if argv[0] == "exec" {
			<-ctx.Done()
			return Result{}, ctx.Err()
		}
		return Result{}, nil
	}}
	sb := create(t, cli, sandbox.Options{}, nil)
	res, err := sb.Exec(context.Background(), core.ExecRequest{Argv: []string{"sleep", "30"}, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("timeout is a result, got err %v", err)
	}
	if !res.TimedOut {
		t.Fatalf("TimedOut = false: %+v", res)
	}
}

func TestExecTruncatesAndRedacts(t *testing.T) {
	cli := &fakeCLI{hook: func(_ context.Context, argv []string, _ string) (Result, error) {
		if argv[0] == "run" {
			return Result{Stdout: "cid123\n"}, nil
		}
		return Result{Stdout: strings.Repeat("a", 100), Stderr: "pw=hunter2-literal extra"}, nil
	}}
	sb := create(t, cli, sandbox.Options{MaxOutputBytes: 64, Redactor: redact.New("hunter2-literal")}, nil)
	res, err := sb.Exec(context.Background(), core.ExecRequest{Argv: []string{"echo"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Stdout) != 64 {
		t.Errorf("truncation: %+v", res)
	}
	if strings.Contains(res.Stderr, "hunter2-literal") || !strings.Contains(res.Stderr, "[REDACTED:") {
		t.Errorf("stderr not redacted: %q", res.Stderr)
	}
}

func TestSnapshotCommitsImage(t *testing.T) {
	cli := &fakeCLI{}
	sb := create(t, cli, sandbox.Options{}, nil)
	var snap core.Snapshotter = sb
	ref, err := snap.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ref.Provider != Name || !strings.HasPrefix(ref.ID, "friday-snap-") || ref.CreatedAt.IsZero() {
		t.Fatalf("ref = %+v", ref)
	}
	commit := cli.last("commit")
	if len(commit) != 3 || commit[0] != "commit" || commit[1] != "cid123" || commit[2] != ref.ID {
		t.Fatalf("commit argv %v, id %s", commit, ref.ID)
	}
}

func TestDestroyRemovesContainer(t *testing.T) {
	cli := &fakeCLI{}
	sb := create(t, cli, sandbox.Options{}, nil)
	dir := sb.Dir()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := sb.Destroy(ctx); err != nil {
			t.Fatalf("destroy #%d: %v", i+1, err)
		}
	}
	rms := cli.all("rm")
	if len(rms) != 1 || !hasPair(rms[0], "-f", "cid123") {
		t.Fatalf("rm calls = %v", rms)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("copy not removed: %v", err)
	}
	if _, err := sb.Exec(ctx, core.ExecRequest{Argv: []string{"true"}}); !errors.Is(err, core.ErrUnavailable) {
		t.Errorf("exec after destroy: %v", err)
	}
	if _, err := sb.ReadFile(ctx, "main.go"); !errors.Is(err, core.ErrUnavailable) {
		t.Errorf("read after destroy: %v", err)
	}
	if len(sb.Enforced()) == 0 {
		t.Error("Enforced() must name network and cgroup limits")
	}
}

func TestKeepWorkspaceKeepsCopy(t *testing.T) {
	cli := &fakeCLI{}
	sb := create(t, cli, sandbox.Options{KeepWorkspace: true}, nil)
	dir := sb.Dir()
	if err := sb.Destroy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cli.all("rm")) != 1 {
		t.Errorf("container must still be removed: %v", cli.all("rm"))
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("KeepWorkspace removed the copy: %v", err)
	}
}

func TestFileAccessConfined(t *testing.T) {
	cli := &fakeCLI{}
	sb := create(t, cli, sandbox.Options{}, nil)
	ctx := context.Background()
	var fa core.FileAccess = sb
	if err := fa.WriteFile(ctx, "new/dir/file.txt", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := fa.ReadFile(ctx, "new/dir/file.txt"); err != nil || string(b) != "x" {
		t.Fatalf("round trip: %q %v", b, err)
	}
	for _, bad := range []string{"../escape", "/etc/passwd", ""} {
		if _, err := fa.ReadFile(ctx, bad); !errors.Is(err, core.ErrInvalidInput) {
			t.Errorf("read %q: want ErrInvalidInput, got %v", bad, err)
		}
		if err := fa.WriteFile(ctx, bad, nil, 0o600); !errors.Is(err, core.ErrInvalidInput) {
			t.Errorf("write %q: want ErrInvalidInput, got %v", bad, err)
		}
	}
	if _, err := fa.ReadFile(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := sb.ReadFile(ctx, "sub"); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("directory read: %v", err)
	}
}

func contains(argv []string, v string) bool {
	for _, a := range argv {
		if a == v {
			return true
		}
	}
	return false
}

func TestCLIRunnerCapturesExit(t *testing.T) {
	r := CLIRunner("sh")
	res, err := r(context.Background(), []string{"-c", "printf out; printf err >&2; exit 3"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || res.Stdout != "out" || res.Stderr != "err" {
		t.Fatalf("%+v", res)
	}
	res, err = r(context.Background(), []string{"-c", "cat"}, "hello")
	if err != nil || res.Stdout != "hello" || res.ExitCode != 0 {
		t.Fatalf("stdin %+v %v", res, err)
	}
}
