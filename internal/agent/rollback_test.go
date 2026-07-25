package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

func TestUpdaterRefusesQuarantinedBuild(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agent.exe")
	if err := os.WriteFile(exe, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := &SelfUpdater{CurrentBuild: 1, Quarantined: 2}
	sm := shared.SignedManifest{Manifest: shared.Manifest{Build: 2, URL: "/agent/download"}}
	// The quarantine check runs before signature/hash work, so it fires even on
	// an unsigned manifest — and must never touch the running binary.
	if err := u.prepare(context.Background(), sm, exe); err != ErrQuarantined {
		t.Fatalf("prepare on quarantined build = %v, want ErrQuarantined", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "current" {
		t.Fatal("exe must be untouched for a quarantined build")
	}
}

func TestEvaluateStartupTrialCommitAndRollback(t *testing.T) {
	dir := t.TempDir()
	now := int64(1_000_000)

	// Fresh install of build 100 → probation, no rollback.
	if d := EvaluateStartup(dir, 100, now); d.Rollback {
		t.Fatal("fresh build should not roll back")
	}
	// It checks in → committed; later restarts are not crashes.
	MarkCheckedIn(dir, 100)
	if d := EvaluateStartup(dir, 100, now+3600); d.Rollback {
		t.Fatal("committed build restart should not roll back")
	}

	// Update to build 200 that never checks in and crash-loops within the window.
	if d := EvaluateStartup(dir, 200, now+10); d.Rollback { // start 1 of the new build
		t.Fatal("first start of the new build should not roll back")
	}
	if d := EvaluateStartup(dir, 200, now+20); d.Rollback { // start 2
		t.Fatal("second start should not roll back yet")
	}
	d := EvaluateStartup(dir, 200, now+30) // start 3, still inside the window
	if !d.Rollback || d.BadBuild != 200 || d.Quarantined != 200 {
		t.Fatalf("third start should roll back build 200; got %+v", d)
	}

	// After rollback, the restored old build starts fresh and the bad build
	// stays quarantined so the updater refuses it.
	d2 := EvaluateStartup(dir, 100, now+40)
	if d2.Rollback {
		t.Fatal("restored old build should not roll back")
	}
	if d2.Quarantined != 200 {
		t.Fatalf("quarantine should persist as 200, got %d", d2.Quarantined)
	}
}

func TestGracefulRestartsAreNotCrashes(t *testing.T) {
	dir := t.TempDir()
	now := int64(1_000_000)
	EvaluateStartup(dir, 200, now) // first start of a new build
	// Rapid restarts that each shut down gracefully (reboots) must NOT roll back,
	// even without a check-in (e.g. the backend is temporarily unreachable).
	for i := int64(1); i <= 4; i++ {
		MarkCleanExit(dir, 200)
		if d := EvaluateStartup(dir, 200, now+i*30); d.Rollback {
			t.Fatalf("graceful restart %d must not roll back", i)
		}
	}
}

func TestUngracefulRestartsStillRollBack(t *testing.T) {
	dir := t.TempDir()
	now := int64(1_000_000)
	EvaluateStartup(dir, 200, now)    // start 1 (no MarkCleanExit = crash)
	EvaluateStartup(dir, 200, now+10) // start 2 (crash)
	if d := EvaluateStartup(dir, 200, now+20); !d.Rollback {
		t.Fatal("three ungraceful restarts within the window should roll back")
	}
}

func TestCrashLoopOutsideWindowIsNotRolledBack(t *testing.T) {
	dir := t.TempDir()
	now := int64(1_000_000)
	EvaluateStartup(dir, 300, now)    // start 1
	EvaluateStartup(dir, 300, now+10) // start 2
	// A third start long after the first is a slow restart, not a crash loop.
	if d := EvaluateStartup(dir, 300, now+crashWindowSecs+100); d.Rollback {
		t.Fatal("slow restarts outside the window must not trip a rollback")
	}
}

func TestRollbackToOldRestoresPrevious(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agent.exe")
	if err := os.WriteFile(exe, []byte("BAD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe+".old", []byte("GOOD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RollbackToOld(exe); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "GOOD" {
		t.Fatalf("exe = %q, want GOOD", got)
	}
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatal(".old should be consumed by the restore")
	}

	// With no .old, rollback errors and leaves the exe untouched.
	solo := filepath.Join(dir, "solo.exe")
	_ = os.WriteFile(solo, []byte("X"), 0o755)
	if err := RollbackToOld(solo); err == nil {
		t.Fatal("want error when there is no .old to restore")
	}
	if got, _ := os.ReadFile(solo); string(got) != "X" {
		t.Fatal("exe must be untouched when there is nothing to restore")
	}
}
