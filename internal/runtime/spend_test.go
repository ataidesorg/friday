package runtime_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ataidesorg/ink/internal/core"
	"github.com/ataidesorg/ink/internal/runtime"
)

func usd(m int64) *core.USDMicros { v := core.USDMicros(m); return &v }

func TestSpendLedgerRoundtrip(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "state", "spend.jsonl")
	sp, err := runtime.NewSpend(0, 0, ledger)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []runtime.SpendEntry{
		{Date: "2026-08-24", Run: "r1", USDMicros: 100},
		{Date: "2026-08-24", Run: "r2", USDMicros: 250},
		{Date: "2026-08-23", Run: "r0", USDMicros: 9999},
	} {
		if err := sp.Commit(e); err != nil {
			t.Fatal(err)
		}
	}
	total, corrupt, err := sp.DayTotal("2026-08-24")
	if err != nil || corrupt != 0 || total != 350 {
		t.Fatalf("total %d corrupt %d err %v", total, corrupt, err)
	}
	info, err := os.Stat(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("ledger mode %o, want 0600", perm)
	}
}

func TestSpendLedgerCorruptLineSkipped(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "spend.jsonl")
	lines := "not json\n" +
		`{"date":"2026-08-24","run":"r1","usd_micros":-5}` + "\n" +
		`{"date":"2026-08-24","run":"r2","usd_micros":70}` + "\n\n"
	if err := os.WriteFile(ledger, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	sp, err := runtime.NewSpend(0, 0, ledger)
	if err != nil {
		t.Fatal(err)
	}
	total, corrupt, err := sp.DayTotal("2026-08-24")
	if err != nil || corrupt != 2 || total != 70 {
		t.Fatalf("total %d corrupt %d err %v", total, corrupt, err)
	}
}

func TestSpendLedgerMissingIsZero(t *testing.T) {
	sp, err := runtime.NewSpend(0, 0, filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	total, corrupt, err := sp.DayTotal("2026-08-24")
	if err != nil || corrupt != 0 || total != 0 {
		t.Fatalf("total %d corrupt %d err %v", total, corrupt, err)
	}
}

func TestSpendSessionUnknownIsSticky(t *testing.T) {
	sp, err := runtime.NewSpend(1, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	sp.AddSession(usd(40))
	sp.AddSession(nil)
	sp.AddSession(usd(60))
	total, exact := sp.SessionTotal()
	if exact || total != 40 {
		t.Fatalf("total %d exact %v, want 40 inexact", total, exact)
	}
}

func TestNewSpendRejectsInvalidCaps(t *testing.T) {
	if _, err := runtime.NewSpend(-1, 0, ""); err == nil || !strings.Contains(err.Error(), "per_session_usd") {
		t.Fatalf("err = %v", err)
	}
	if _, err := runtime.NewSpend(0, -1, ""); err == nil || !strings.Contains(err.Error(), "per_day_usd") {
		t.Fatalf("err = %v", err)
	}
}
