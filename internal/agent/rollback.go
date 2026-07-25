package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Crash-loop auto-rollback (PLAN.md §5.4). A silent, self-updating agent that
// swaps to a valid-but-broken build would relaunch it forever (the Scheduled
// Task's RestartOnFailure never gives up). So a freshly-updated build is on
// probation until it checks in once: if it instead restarts crashThreshold
// times within crashWindow without ever reaching a successful ingest, it is
// deemed bad — the previous binary (agent.exe.old) is restored and the bad
// build is quarantined so the updater won't immediately re-apply it.

const (
	updateStateFile = "update-state.json"
	crashWindowSecs = 10 * 60 // restarts within this window count toward the loop
	crashThreshold  = 3       // starts without a check-in that trip a rollback
)

// UpdateState persists across restarts in the data dir.
type UpdateState struct {
	Build       int64 `json:"build"`       // the build this trial tracks
	FirstStart  int64 `json:"first_start"` // unix of the first start of Build
	StartCount  int   `json:"start_count"` // starts of Build with no check-in yet
	Committed   bool  `json:"committed"`   // has Build ever checked in?
	Quarantined int64 `json:"quarantined"` // a known-bad build the updater must refuse
}

func loadUpdateState(dir string) UpdateState {
	var s UpdateState
	if data, err := os.ReadFile(filepath.Join(dir, updateStateFile)); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

func saveUpdateState(dir string, s UpdateState) {
	if data, err := json.MarshalIndent(s, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, updateStateFile), data, 0o600)
	}
}

// StartupDecision tells main() what to do at startup.
type StartupDecision struct {
	Rollback    bool  // restore agent.exe.old and relaunch
	BadBuild    int64 // the crash-looping build (quarantined)
	Quarantined int64 // build the updater must refuse this run (carried across restarts)
}

// EvaluateStartup records this start and reports whether the current build is
// crash-looping and must be rolled back. `now` is unix seconds (injectable).
func EvaluateStartup(dir string, currentBuild, now int64) StartupDecision {
	s := loadUpdateState(dir)
	quarantined := s.Quarantined

	// A different build than we were tracking = a fresh install, an applied
	// update, or the restored old build after a rollback. Start its probation.
	if s.Build != currentBuild {
		saveUpdateState(dir, UpdateState{
			Build: currentBuild, FirstStart: now, StartCount: 1, Quarantined: quarantined,
		})
		return StartupDecision{Quarantined: quarantined}
	}

	// Already proven good — nothing to police.
	if s.Committed {
		return StartupDecision{Quarantined: quarantined}
	}

	// Same build, still no check-in: this restart may be a crash.
	s.StartCount++
	if s.StartCount >= crashThreshold && now-s.FirstStart <= crashWindowSecs {
		// Crash loop. Quarantine this build; the restored old build (a different
		// build number) will reset the trial on its next start.
		saveUpdateState(dir, UpdateState{Quarantined: currentBuild})
		return StartupDecision{Rollback: true, BadBuild: currentBuild, Quarantined: currentBuild}
	}
	saveUpdateState(dir, s)
	return StartupDecision{Quarantined: quarantined}
}

// MarkCheckedIn commits the current build after its first successful check-in so
// later restarts aren't mistaken for crashes.
func MarkCheckedIn(dir string, currentBuild int64) {
	s := loadUpdateState(dir)
	if s.Build == currentBuild && !s.Committed {
		s.Committed = true
		saveUpdateState(dir, s)
	}
}

// RollbackToOld restores agent.exe.old over exe — the reverse of the update
// swap — so a crash-looping build is replaced by the last-known-good binary.
// Returns an error (leaving exe untouched) if there is nothing to roll back to.
func RollbackToOld(exe string) error {
	old := exe + ".old"
	if _, err := os.Stat(old); err != nil {
		return err
	}
	bad := exe + ".bad"
	_ = os.Remove(bad)
	if err := os.Rename(exe, bad); err != nil {
		return err
	}
	if err := os.Rename(old, exe); err != nil {
		_ = os.Rename(bad, exe) // restore failed — keep the (bad) current running
		return err
	}
	_ = os.Remove(bad)
	return nil
}
