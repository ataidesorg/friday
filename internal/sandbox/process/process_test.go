package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/redact"
	"github.com/ataidesorg/ink/internal/sandbox"
)

// secretish is assembled from fragments so the repo never holds a secret-shaped literal.
var secretish = "ghp_" + strings.Repeat("a1B2", 9)

func needSh(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh missing")
	}
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
	write(".git/HEAD", "ref: refs/heads/main\n")
	write("node_modules/x/index.js", "x\n")
	write("debug.log", "log\n")
	if err := os.Chmod(filepath.Join(root, "main.go"), 0o700); err != nil { //nolint:gosec // exec bit under test
		t.Fatal(err)
	}
	if err := os.Symlink("sub/notes.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	return root
}

func create(t *testing.T, o sandbox.Options, mod func(*core.SandboxSpec)) *Sandbox {
	t.Helper()
	spec := core.NewSandboxSpec(fixture(t))
	if mod != nil {
		mod(&spec)
	}
	sb, err := New(o).Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Destroy(context.Background()) })
	return sb.(*Sandbox)
}

func run(t *testing.T, sb *Sandbox, req core.ExecRequest) core.ExecResult {
	t.Helper()
	res, err := sb.Exec(context.Background(), req)
	if err != nil {
		t.Fatalf("exec %v: %v", req.Argv, err)
	}
	return res
}

func sh(script string) core.ExecRequest { return core.ExecRequest{Argv: []string{"sh", "-c", script}} }

func TestCreateCopiesWorkspace(t *testing.T) {
	sb := create(t, sandbox.Options{}, nil)
	info := sb.Info()
	if info.Provider != "process" || info.ID == "" {
		t.Fatalf("info = %+v", info)
	}
	if sb.Dir() == info.Spec.WorkDir {
		t.Fatal("copy mode must not run in the caller's tree")
	}
	for _, rel := range []string{"main.go", "sub/notes.txt", ".git/HEAD", "node_modules/x/index.js", "debug.log"} {
		if _, err := os.Stat(filepath.Join(sb.Dir(), rel)); err != nil {
			t.Errorf("%s not copied: %v", rel, err)
		}
	}
	if st, err := os.Stat(filepath.Join(sb.Dir(), "main.go")); err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Errorf("exec bit not preserved: %v %v", st, err)
	}
	if target, err := os.Readlink(filepath.Join(sb.Dir(), "link")); err != nil || target != "sub/notes.txt" {
		t.Errorf("symlink not recreated: %q %v", target, err)
	}
	if p := New(sandbox.Options{}); p.Name() != "process" {
		t.Errorf("Name = %q", p.Name())
	}
}

func TestCreateHonoursExclude(t *testing.T) {
	sb := create(t, sandbox.Options{}, func(s *core.SandboxSpec) {
		s.Source.Exclude = []string{"node_modules/**", "*.log"}
	})
	for _, rel := range []string{"node_modules", "debug.log"} {
		if _, err := os.Stat(filepath.Join(sb.Dir(), rel)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should be excluded: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(sb.Dir(), "sub/notes.txt")); err != nil {
		t.Errorf("unexcluded file missing: %v", err)
	}
}

func TestCreateFailsClosed(t *testing.T) {
	p := New(sandbox.Options{})
	ctx := context.Background()
	cases := []struct {
		name string
		mod  func(*core.SandboxSpec)
		want error
	}{
		{"secret env", func(s *core.SandboxSpec) { s.Env = map[string]string{"TOKEN": secretish} }, core.ErrSecretContent},
		{"relative workdir", func(s *core.SandboxSpec) { s.WorkDir = "rel" }, core.ErrInvalidInput},
		{"missing workdir", func(s *core.SandboxSpec) { s.WorkDir = filepath.Join(s.WorkDir, "nope") }, core.ErrNotFound},
		{"mounts", func(s *core.SandboxSpec) { s.Mounts = []core.Mount{{Host: "/a", Guest: "/b"}} }, core.ErrNotImplemented},
	}
	for _, c := range cases {
		spec := core.NewSandboxSpec(fixture(t))
		c.mod(&spec)
		if _, err := p.Create(ctx, spec); !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", c.name, c.want, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.Create(cancelled, core.NewSandboxSpec(fixture(t))); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled create: %v", err)
	}
}

func TestExecEnvIsScrubbed(t *testing.T) {
	needSh(t)
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	t.Setenv("INK_HOST_ONLY", "1")
	hostPath := os.Getenv("PATH")
	sb := create(t, sandbox.Options{}, func(s *core.SandboxSpec) {
		s.Env = map[string]string{"FOO": "bar", "http_proxy": "http://proxy.example:3128", "HOME": "/tmp/evil"}
	})
	out := run(t, sb, sh("env")).Stdout
	has := func(line string) bool { return strings.Contains(out, line+"\n") }
	for _, want := range []string{"HOME=" + sb.home, "TMPDIR=" + filepath.Join(sb.home, "tmp"), "FOO=bar", "NO_PROXY=*", "GOPROXY=off", "GOTOOLCHAIN=local"} {
		if !has(want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, forbid := range []string{"HTTPS_PROXY=", "http_proxy=", "INK_HOST_ONLY=", "PATH=" + hostPath + "\n", "HOME=/tmp/evil"} {
		if strings.Contains(out, forbid) {
			t.Errorf("leaked %q in:\n%s", forbid, out)
		}
	}
	if _, err := os.Stat(filepath.Join(sb.home, "tmp")); err != nil {
		t.Errorf("TMPDIR not created: %v", err)
	}
}

func TestExecBasics(t *testing.T) {
	needSh(t)
	sb := create(t, sandbox.Options{}, nil)
	if res := run(t, sb, sh("exit 3")); res.ExitCode != 3 || res.TimedOut || res.Truncated {
		t.Errorf("exit code: %+v", res)
	}
	if res := run(t, sb, core.ExecRequest{Argv: []string{"cat"}, Stdin: "hi"}); res.Stdout != "hi" {
		t.Errorf("stdin: %+v", res)
	}
	if res := run(t, sb, core.ExecRequest{Argv: []string{"pwd"}, Dir: "sub"}); strings.TrimSpace(res.Stdout) != filepath.Join(sb.Dir(), "sub") {
		t.Errorf("dir: %+v", res)
	}
	if res := run(t, sb, sh("echo out; echo err >&2")); res.Stdout != "out\n" || res.Stderr != "err\n" || res.Elapsed <= 0 {
		t.Errorf("streams: %+v", res)
	}
	ctx := context.Background()
	for _, c := range []struct {
		req  core.ExecRequest
		want error
	}{
		{core.ExecRequest{}, core.ErrInvalidInput},
		{core.ExecRequest{Argv: []string{"definitely-not-a-binary-xyz"}}, core.ErrNotFound},
		{core.ExecRequest{Argv: []string{"true"}, Dir: "../outside"}, core.ErrInvalidInput},
		{core.ExecRequest{Argv: []string{"true"}, Dir: "/etc"}, core.ErrInvalidInput},
		{core.ExecRequest{Argv: []string{"true"}, Timeout: -1}, core.ErrInvalidInput},
	} {
		if _, err := sb.Exec(ctx, c.req); !errors.Is(err, c.want) {
			t.Errorf("%+v: want %v, got %v", c.req, c.want, err)
		}
	}
}

func TestExecTimeoutKillsProcessGroup(t *testing.T) {
	needSh(t)
	sb := create(t, sandbox.Options{}, nil)
	start := time.Now()
	res := run(t, sb, core.ExecRequest{Argv: []string{"sh", "-c", "sleep 30 & echo $!; wait"}, Timeout: 100 * time.Millisecond})
	if !res.TimedOut || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout not enforced: %+v after %s", res, time.Since(start))
	}
	pid, err := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if err != nil {
		t.Fatalf("no child pid in %q", res.Stdout)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("background child %d survived the timeout", pid)
}

func TestExecTimeoutCapsAtLimits(t *testing.T) {
	needSh(t)
	sb := create(t, sandbox.Options{}, func(s *core.SandboxSpec) { s.Limits.WallClock = 100 * time.Millisecond })
	if res := run(t, sb, core.ExecRequest{Argv: []string{"sleep", "5"}, Timeout: time.Minute}); !res.TimedOut {
		t.Errorf("Limits.WallClock not applied: %+v", res)
	}
}

func TestExecCancelledContext(t *testing.T) {
	needSh(t)
	sb := create(t, sandbox.Options{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sb.Exec(ctx, sh("true")); !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestExecTruncatesAndRedacts(t *testing.T) {
	needSh(t)
	sb := create(t, sandbox.Options{MaxOutputBytes: 64, Redactor: redact.New("hunter2-literal")}, nil)
	res := run(t, sb, sh("yes | head -c 1000; yes | head -c 1000 >&2"))
	if !res.Truncated || len(res.Stdout) != 64 || len(res.Stderr) != 64 {
		t.Errorf("truncation: %+v", res)
	}
	res = run(t, sb, sh("echo token="+secretish+"; echo pw=hunter2-literal >&2"))
	if strings.Contains(res.Stdout, secretish) || !strings.Contains(res.Stdout, "[REDACTED:") {
		t.Errorf("stdout not redacted: %q", res.Stdout)
	}
	if strings.Contains(res.Stderr, "hunter2-literal") {
		t.Errorf("stderr literal not redacted: %q", res.Stderr)
	}
}

func TestSandboxLifetime(t *testing.T) {
	needSh(t)
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return now }
	sb := create(t, sandbox.Options{Clock: clock}, func(s *core.SandboxSpec) { s.WallClock = time.Minute })
	run(t, sb, sh("true"))
	now = now.Add(2 * time.Minute)
	if _, err := sb.Exec(context.Background(), sh("true")); !errors.Is(err, core.ErrTimeout) {
		t.Errorf("expired sandbox: want ErrTimeout, got %v", err)
	}
}

func TestFileAccessConfined(t *testing.T) {
	sb := create(t, sandbox.Options{}, nil)
	ctx := context.Background()
	var fa core.FileAccess = sb
	if err := fa.WriteFile(ctx, "new/dir/file.txt", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := fa.ReadFile(ctx, "new/dir/file.txt"); err != nil || string(b) != "x" {
		t.Fatalf("round trip: %q %v", b, err)
	}
	if b, err := fa.ReadFile(ctx, "link"); err != nil || string(b) != "notes\n" {
		t.Errorf("symlink inside tree: %q %v", b, err)
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

func TestSnapshotNotImplemented(t *testing.T) {
	sb := create(t, sandbox.Options{}, nil)
	var snap core.Snapshotter = sb
	_, err := snap.Snapshot(context.Background())
	var nie core.NotImplementedError
	if !errors.Is(err, core.ErrNotImplemented) || !errors.As(err, &nie) || nie.Feature != "process snapshot" {
		t.Errorf("snapshot: %v", err)
	}
}

func TestDestroy(t *testing.T) {
	needSh(t)
	ctx := context.Background()
	sb := create(t, sandbox.Options{}, nil)
	dir := sb.Dir()
	for i := 0; i < 2; i++ {
		if err := sb.Destroy(ctx); err != nil {
			t.Fatalf("destroy #%d: %v", i+1, err)
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("copy not removed: %v", err)
	}
	if len(sb.Enforced()) == 0 {
		t.Error("Enforced() must name what is enforced")
	}
	if _, err := sb.Exec(ctx, sh("true")); !errors.Is(err, core.ErrUnavailable) {
		t.Errorf("exec after destroy: %v", err)
	}
	if _, err := sb.ReadFile(ctx, "main.go"); !errors.Is(err, core.ErrUnavailable) {
		t.Errorf("read after destroy: %v", err)
	}

	kept := create(t, sandbox.Options{KeepWorkspace: true}, nil)
	if err := kept.Destroy(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kept.Dir()); err != nil {
		t.Errorf("KeepWorkspace removed the copy: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(kept.home) })

	inPlace := create(t, sandbox.Options{}, func(s *core.SandboxSpec) { s.Source.Kind = core.SourceInPlace })
	if want, _ := filepath.EvalSymlinks(inPlace.Info().Spec.WorkDir); inPlace.Dir() != want {
		t.Fatalf("in_place must run in WorkDir: %s != %s", inPlace.Dir(), want)
	}
	run(t, inPlace, sh("echo touched > touched.txt"))
	if err := inPlace.Destroy(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(inPlace.Dir(), "touched.txt")); err != nil {
		t.Errorf("in_place destroy must leave the tree alone: %v", err)
	}
}

func TestGoTestRunsInsideCopy(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on host PATH")
	}
	src, err := filepath.Abs(filepath.Join("..", "..", "..", "test", "sample-project"))
	if err != nil {
		t.Fatal(err)
	}
	spec := core.NewSandboxSpec(src)
	sb, err := New(sandbox.Options{}).Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Destroy(context.Background()) })
	res, err := sb.Exec(context.Background(), core.ExecRequest{Argv: []string{"go", "test", "./..."}, Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "ok") {
		t.Fatalf("go test in sandbox failed: %+v", res)
	}
}

func TestScrubEnv(t *testing.T) {
	r := redact.New()
	got, err := scrubEnv(map[string]string{"A": "1", "HTTP_PROXY": "x", "no_proxy": "y", "ALL_PROXY": "z"}, r)
	if err != nil || len(got) != 1 || got["A"] != "1" {
		t.Errorf("scrubEnv = %v, %v", got, err)
	}
	if _, err := scrubEnv(map[string]string{"K": secretish}, r); !errors.Is(err, core.ErrSecretContent) {
		t.Errorf("secret value: %v", err)
	}
	if _, err := scrubEnv(map[string]string{"": "v"}, r); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("empty key: %v", err)
	}
	if _, err := scrubEnv(map[string]string{"A=B": "v"}, r); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("key with '=': %v", err)
	}
}

func TestLookPathInsideTree(t *testing.T) {
	needSh(t)
	sb := create(t, sandbox.Options{}, nil)
	ctx := context.Background()
	script := filepath.Join(sb.Dir(), "sub", "hello.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hello\n"), 0o700); err != nil { //nolint:gosec // test script must be executable
		t.Fatal(err)
	}
	if res := run(t, sb, core.ExecRequest{Argv: []string{"./hello.sh"}, Dir: "sub"}); res.Stdout != "hello\n" {
		t.Errorf("relative script: %+v", res)
	}
	if res := run(t, sb, core.ExecRequest{Argv: []string{script}}); res.Stdout != "hello\n" {
		t.Errorf("absolute script inside tree: %+v", res)
	}
	for _, c := range []struct {
		req  core.ExecRequest
		want error
	}{
		{core.ExecRequest{Argv: []string{"./sub/notes.txt"}}, core.ErrNotFound},
		{core.ExecRequest{Argv: []string{"./main.go"}}, core.ErrUnavailable},
		{core.ExecRequest{Argv: []string{"../../../../../bin/sh"}}, core.ErrInvalidInput},
		{core.ExecRequest{Argv: []string{"/bin/sh", "-c", "true"}}, core.ErrInvalidInput},
		{core.ExecRequest{Argv: []string{"true"}, Dir: "missing"}, core.ErrNotFound},
		{core.ExecRequest{Argv: []string{"true"}, Dir: "main.go"}, core.ErrInvalidInput},
	} {
		if _, err := sb.Exec(ctx, c.req); !errors.Is(err, c.want) {
			t.Errorf("%v: want %v, got %v", c.req.Argv, c.want, err)
		}
	}
}

func TestHostCachesAndPath(t *testing.T) {
	t.Setenv("GOCACHE", "/c/go-build")
	t.Setenv("GOMODCACHE", "/c/mod")
	t.Setenv("GOROOT", "/opt/go")
	if got := hostGoCache(); got != "/c/go-build" {
		t.Errorf("GOCACHE = %q", got)
	}
	if got := hostGoModCache(); got != "/c/mod" {
		t.Errorf("GOMODCACHE = %q", got)
	}
	if dirs := pathDirs(); !slices.Contains(dirs, "/opt/go/bin") {
		t.Errorf("GOROOT/bin missing from %v", dirs)
	}
	t.Setenv("GOMODCACHE", "")
	t.Setenv("GOPATH", "/gp1"+string(os.PathListSeparator)+"/gp2")
	if got := hostGoModCache(); got != "/gp1/pkg/mod" {
		t.Errorf("GOPATH-derived = %q", got)
	}
	t.Setenv("GOPATH", "")
	t.Setenv("GOCACHE", "")
	if got := hostGoModCache(); !strings.HasSuffix(got, filepath.Join("go", "pkg", "mod")) {
		t.Errorf("HOME-derived = %q", got)
	}
	if got := hostGoCache(); !strings.HasSuffix(got, "go-build") {
		t.Errorf("cache-dir-derived = %q", got)
	}
}

func TestCreateCopyFailures(t *testing.T) {
	p := New(sandbox.Options{})
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Create(ctx, core.NewSandboxSpec(file)); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("file as work_dir: %v", err)
	}
	root := fixture(t)
	if err := os.Chmod(filepath.Join(root, "sub"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "sub"), 0o700) }) //nolint:gosec // restore so TempDir cleanup works
	if _, err := p.Create(ctx, core.NewSandboxSpec(root)); err == nil || !strings.Contains(err.Error(), "copy workspace") {
		t.Errorf("unreadable dir must fail the copy: %v", err)
	}
	fifo := filepath.Join(fixture(t), "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo unavailable")
	}
	sb := create(t, sandbox.Options{}, func(s *core.SandboxSpec) { s.WorkDir = filepath.Dir(fifo) })
	if _, err := os.Lstat(filepath.Join(sb.Dir(), "pipe")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("special file must be skipped: %v", err)
	}
}

func TestWriteFileFailures(t *testing.T) {
	sb := create(t, sandbox.Options{}, nil)
	ctx := context.Background()
	if err := sb.WriteFile(ctx, "main.go/child", nil, 0o600); err == nil {
		t.Error("writing under a file must fail")
	}
	if err := sb.WriteFile(ctx, "sub/../link", []byte("new"), 0o600); err != nil {
		t.Errorf("write through in-tree symlink: %v", err)
	}
	if b, err := sb.ReadFile(ctx, "sub/notes.txt"); err != nil || string(b) != "new" {
		t.Errorf("symlink write did not reach target: %q %v", b, err)
	}
	if err := os.Symlink("/etc", filepath.Join(sb.Dir(), "out")); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.ReadFile(ctx, "out/hosts"); !errors.Is(err, core.ErrInvalidInput) {
		t.Errorf("symlink out of tree must be refused: %v", err)
	}
	if err := sb.Destroy(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sb.WriteFile(ctx, "x", nil, 0o600); !errors.Is(err, core.ErrUnavailable) {
		t.Errorf("write after destroy: %v", err)
	}
}

func TestPathDirsPrefersHostToolchain(t *testing.T) {
	p, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on host PATH")
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	want := filepath.Dir(p)
	dirs := pathDirs()
	at := slices.Index(dirs, want)
	if at < 0 {
		t.Fatalf("pathDirs %v missing host go dir %q", dirs, want)
	}
	for _, d := range systemPath {
		if i := slices.Index(dirs, d); i >= 0 && i < at {
			t.Errorf("system dir %q precedes host go dir %q in %v", d, want, dirs)
		}
	}
}
