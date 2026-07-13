package server

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), "roll.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// day 2023-11-14 00:00:00 UTC
const dayBase = int64(1_699_920_000)

func TestMonitorOnSecondsIntegratesIntervals(t *testing.T) {
	st := openTestStore(t)
	id, err := st.UpsertDevice("dev", "host", "1", dayBase)
	if err != nil {
		t.Fatal(err)
	}
	// on at +100s, off at +400s  → 300s on inside the window.
	_, _, err = st.InsertMonitorEvents(id, []shared.MonitorEvent{
		{ClientTS: dayBase + 100, MonitorOn: 1},
		{ClientTS: dayBase + 400, MonitorOn: 0},
	}, dayBase+400)
	if err != nil {
		t.Fatal(err)
	}
	secs, err := st.MonitorOnSeconds(id, dayBase, dayBase+86400)
	if err != nil {
		t.Fatal(err)
	}
	if secs != 300 {
		t.Fatalf("MonitorOnSeconds = %d, want 300", secs)
	}
}

func TestMonitorOnSecondsCarriesStateAcrossWindowStart(t *testing.T) {
	st := openTestStore(t)
	id, _ := st.UpsertDevice("dev", "host", "1", dayBase)
	// Turned on before the window and never turned off → on for the whole window.
	_, _, err := st.InsertMonitorEvents(id, []shared.MonitorEvent{
		{ClientTS: dayBase - 3600, MonitorOn: 1},
	}, dayBase)
	if err != nil {
		t.Fatal(err)
	}
	secs, _ := st.MonitorOnSeconds(id, dayBase, dayBase+600)
	if secs != 600 {
		t.Fatalf("MonitorOnSeconds = %d, want 600 (state carried from before window)", secs)
	}
}

func TestRollupDayFromSamplesAndEvents(t *testing.T) {
	st := openTestStore(t)
	id, _ := st.UpsertDevice("dev", "host", "1", dayBase)

	// 4 samples this day: 3 monitor-on (2 active, 1 idle), 1 monitor-off.
	_, _, err := st.InsertSamples(id, []shared.Sample{
		{ClientTS: dayBase + 60, MonitorOn: 1, IsIdle: false, Exe: "game.exe"},
		{ClientTS: dayBase + 120, MonitorOn: 1, IsIdle: false, Exe: "game.exe"},
		{ClientTS: dayBase + 180, MonitorOn: 1, IsIdle: true, Exe: "chrome.exe"},
		{ClientTS: dayBase + 240, MonitorOn: 0, IsIdle: true, Exe: "chrome.exe"},
	}, dayBase+240)
	if err != nil {
		t.Fatal(err)
	}
	// Exact events: on for 600s (= 10 min) that day.
	_, _, err = st.InsertMonitorEvents(id, []shared.MonitorEvent{
		{ClientTS: dayBase + 60, MonitorOn: 1},
		{ClientTS: dayBase + 660, MonitorOn: 0},
	}, dayBase+660)
	if err != nil {
		t.Fatal(err)
	}

	day := DayUTC(dayBase + 60)
	if err := st.RollupDay(id, day, dayBase+86400); err != nil {
		t.Fatalf("RollupDay: %v", err)
	}

	var mon, act, sess int
	err = st.db.QueryRow(`SELECT monitor_minutes, active_minutes, session_minutes FROM daily_stats WHERE device_id=? AND day=?`,
		id, day).Scan(&mon, &act, &sess)
	if err != nil {
		t.Fatal(err)
	}
	if mon != 10 { // exact from events (600s → 10 min), not the 3-sample approximation
		t.Fatalf("monitor_minutes = %d, want 10 (exact from events)", mon)
	}
	if act != 2 || sess != 4 {
		t.Fatalf("active=%d session=%d, want 2/4", act, sess)
	}

	// Top-app rollup: game.exe should have 2 monitor-on minutes.
	var gameMin int
	err = st.db.QueryRow(`SELECT monitor_minutes FROM daily_app_stats WHERE device_id=? AND day=? AND exe_name='game.exe'`,
		id, day).Scan(&gameMin)
	if err != nil {
		t.Fatal(err)
	}
	if gameMin != 2 {
		t.Fatalf("game.exe monitor_minutes = %d, want 2", gameMin)
	}
}

func TestRollupIsIdempotent(t *testing.T) {
	st := openTestStore(t)
	id, _ := st.UpsertDevice("dev", "host", "1", dayBase)
	_, _, _ = st.InsertSamples(id, []shared.Sample{
		{ClientTS: dayBase + 60, MonitorOn: 1, Exe: "a.exe"},
	}, dayBase+60)
	day := DayUTC(dayBase + 60)
	if err := st.RollupDay(id, day, dayBase+86400); err != nil {
		t.Fatal(err)
	}
	if err := st.RollupDay(id, day, dayBase+86400); err != nil { // second run must not double-count
		t.Fatal(err)
	}
	var sess int
	_ = st.db.QueryRow(`SELECT session_minutes FROM daily_stats WHERE device_id=? AND day=?`, id, day).Scan(&sess)
	if sess != 1 {
		t.Fatalf("session_minutes = %d after two rollups, want 1 (idempotent)", sess)
	}
}

func TestServerRunRollupsClearsDirty(t *testing.T) {
	st := openTestStore(t)
	srv := New(st, "")
	srv.now = func() time.Time { return time.Unix(dayBase+300, 0) }

	id, _ := st.UpsertDevice("dev", "host", "1", dayBase)
	_, days, _ := st.InsertSamples(id, []shared.Sample{
		{ClientTS: dayBase + 60, MonitorOn: 1, Exe: "a.exe"},
	}, dayBase+300)
	srv.markDirty(id, days)

	srv.RunRollups()

	srv.mu.Lock()
	n := len(srv.dirty)
	srv.mu.Unlock()
	if n != 0 {
		t.Fatalf("dirty set not cleared: %d remain", n)
	}
	var mon int
	_ = st.db.QueryRow(`SELECT monitor_minutes FROM daily_stats WHERE device_id=?`, id).Scan(&mon)
	if mon != 1 {
		t.Fatalf("rollup not applied: monitor_minutes=%d", mon)
	}
}
