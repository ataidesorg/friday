package policy_test

import (
	"sync"
	"testing"
	"time"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/policy"
)

func pathReq(tool, path string) core.CapabilityRequest {
	return core.CapabilityRequest{Call: core.NewToolCallID(), Tool: tool, Capability: core.Capability{Risk: core.RiskWriteLocal, Scope: core.ResourceScope{Kind: core.ScopePath, Path: path}}}
}

func TestApprovalsScopes(t *testing.T) {
	a := policy.NewApprovals()
	by := core.Principal{Kind: core.PrincipalUser, Name: "owner"}
	once := core.ApprovalResolution{Decision: core.ApprovalApproved, By: by, At: time.Unix(1, 0), Scope: core.ApprovalOnce}
	a.Record(pathReq("write_file", "a.go"), once)
	if _, ok := a.Lookup(pathReq("write_file", "a.go")); ok {
		t.Fatal("once must not be stored")
	}
	session := once
	session.Scope = core.ApprovalSession
	a.Record(pathReq("write_file", "a.go"), session)
	got, ok := a.Lookup(pathReq("write_file", "a.go"))
	if !ok || got.Decision != core.ApprovalApproved || got.By.Name != "owner" {
		t.Fatalf("session hit: %+v %v", got, ok)
	}
	if _, ok := a.Lookup(pathReq("write_file", "b.go")); ok {
		t.Fatal("different path must miss")
	}
	if _, ok := a.Lookup(pathReq("apply_patch", "a.go")); ok {
		t.Fatal("different tool must miss")
	}
	if _, ok := policy.NewApprovals().Lookup(pathReq("write_file", "a.go")); ok {
		t.Fatal("approvals must not leak across instances")
	}
	denied := session
	denied.Decision = core.ApprovalDenied
	a.Record(pathReq("write_file", "a.go"), denied)
	if got, _ := a.Lookup(pathReq("write_file", "a.go")); got.Decision != core.ApprovalDenied {
		t.Fatal("a later session decision replaces the earlier one")
	}
	var nilA *policy.Approvals
	nilA.Record(pathReq("x", "y"), session)
	if _, ok := nilA.Lookup(pathReq("x", "y")); ok {
		t.Fatal("nil store must miss")
	}
}

func TestApprovalsKey(t *testing.T) {
	a := policy.NewApprovals()
	cmd := core.CapabilityRequest{Tool: "run_command", Capability: core.Capability{Risk: core.RiskExecuteLocal, Scope: core.ResourceScope{Kind: core.ScopeCommand, Argv: []string{"go", "test", "./..."}}}}
	if k := a.Key(cmd); k != "run_command|execute_local|command|go\x00test\x00./..." {
		t.Fatalf("command key: %q", k)
	}
	if k := a.Key(pathReq("write_file", "sub/a.go")); k != "write_file|write_local|path|sub/a.go" {
		t.Fatalf("path key: %q", k)
	}
	anyReq := core.CapabilityRequest{Tool: "t", Capability: core.Capability{Risk: core.RiskReadOnly, Scope: core.ResourceScope{Kind: core.ScopeAny}}}
	if k := a.Key(anyReq); k != "t|read_only|any|" {
		t.Fatalf("any key: %q", k)
	}
	emptyArgv := cmd
	emptyArgv.Capability.Scope.Argv = nil
	if k := a.Key(emptyArgv); k != "run_command|execute_local|command|" {
		t.Fatalf("empty argv key: %q", k)
	}
	split := cmd
	split.Capability.Scope.Argv = []string{"go", "test ./..."}
	if a.Key(split) == a.Key(cmd) {
		t.Fatal("distinct argvs must not share an approval key")
	}
	// A session approval covers one exact argv, never every command that
	// shares a program name.
	a.Record(cmd, core.ApprovalResolution{Decision: core.ApprovalApproved, Scope: core.ApprovalSession})
	other := cmd
	other.Capability.Scope.Argv = []string{"go", "build", "./..."}
	if _, ok := a.Lookup(other); ok {
		t.Fatal("approval for `go test ./...` must not cover `go build ./...`")
	}
	if _, ok := a.Lookup(cmd); !ok {
		t.Fatal("identical argv must still hit")
	}
}

func TestApprovalsConcurrent(t *testing.T) {
	a := policy.NewApprovals()
	res := core.ApprovalResolution{Decision: core.ApprovalApproved, Scope: core.ApprovalSession}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(2)
		go func() { defer wg.Done(); a.Record(pathReq("w", "p"), res) }()
		go func() { defer wg.Done(); a.Lookup(pathReq("w", string(rune('a'+i%3)))) }()
	}
	wg.Wait()
	if _, ok := a.Lookup(pathReq("w", "p")); !ok {
		t.Fatal("session approval lost")
	}
}
