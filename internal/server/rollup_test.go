package server

import (
	"database/sql"
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

// The DDC probe's meaningful values include negatives (-1 no handle, -2 query
// failed), so they must round-trip raw — only "not sampled" becomes NULL.
func TestInsertSamplesStoresRawDDCPower(t *testing.T) {
	st := openTestStore(t)
	id, err := st.UpsertDevice("dev-ddc", "host", "1", dayBase)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = st.InsertSamples(id, []shared.Sample{
		{ClientTS: dayBase + 60, DDCPower: -1},  // DP off signature
		{ClientTS: dayBase + 120, DDCPower: 5},  // HDMI off signature
		{ClientTS: dayBase + 180, DDCPower: -3}, // not sampled → NULL
		{ClientTS: dayBase + 240},               // legacy agent (field absent) → NULL
	}, dayBase+300)
	if err != nil {
		t.Fatal(err)
	}

	rows, err := st.db.Query(`SELECT ddc_power FROM samples WHERE device_id=? ORDER BY ts`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []sql.NullInt64
	for rows.Next() {
		var v sql.NullInt64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []sql.NullInt64{
		{Int64: -1, Valid: true},
		{Int64: 5, Valid: true},
		{Valid: false},
		{Valid: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: ddc_power = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDashboardQueries(t *testing.T) {
	st := openTestStore(t)
	id, err := st.UpsertDevice("dev-dash", "host", "1.0", dayBase)
	if err != nil {
		t.Fatal(err)
	}
	h9 := dayBase + 9*3600
	if _, _, err := st.InsertSamples(id, []shared.Sample{
		{ClientTS: h9 + 60, MonitorOn: 1, IsIdle: false, Exe: "game.exe"},
		{ClientTS: h9 + 120, MonitorOn: 1, IsIdle: false, Exe: "game.exe"},
		{ClientTS: h9 + 180, MonitorOn: 1, IsIdle: true, Exe: "chrome.exe"},
		{ClientTS: dayBase + 10*3600 + 60, MonitorOn: 0, IsIdle: true, Exe: "chrome.exe"},
	}, dayBase+11*3600); err != nil {
		t.Fatal(err)
	}
	day := DayUTC(dayBase)
	if err := st.RollupDay(id, day, dayBase+86400); err != nil {
		t.Fatal(err)
	}

	// Trend: household daily totals (sample approximation: 3 monitor-on, 2 active).
	tr, err := st.Trend(day, day)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr) != 1 || tr[0].MonitorMinutes != 3 || tr[0].ActiveMinutes != 2 {
		t.Fatalf("Trend = %+v, want one day 3/2", tr)
	}

	// Timeline: 24 buckets from raw samples; hour 9 has 3 on / 2 active.
	hrs, err := st.DeviceTimeline("dev-dash", dayBase)
	if err != nil {
		t.Fatal(err)
	}
	if len(hrs) != 24 {
		t.Fatalf("timeline buckets = %d, want 24", len(hrs))
	}
	if hrs[9].MonitorMinutes != 3 || hrs[9].ActiveMinutes != 2 {
		t.Fatalf("hour 9 = %+v, want 3/2", hrs[9])
	}
	if hrs[10].MonitorMinutes != 0 {
		t.Fatalf("hour 10 monitor = %d, want 0 (sample was monitor-off)", hrs[10].MonitorMinutes)
	}

	// Top apps: game.exe=2, chrome.exe=1 (only monitor-on samples counted).
	apps, err := st.DeviceTopApps("dev-dash", day, day, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 || apps[0].Exe != "game.exe" || apps[0].MonitorMinutes != 2 {
		t.Fatalf("TopApps = %+v, want game.exe=2 first", apps)
	}
}

func TestDeviceSignalsAndHeatmap(t *testing.T) {
	st := openTestStore(t)
	id, err := st.UpsertDevice("dev-sig", "host", "1.0", dayBase)
	if err != nil {
		t.Fatal(err)
	}
	h9 := dayBase + 9*3600
	// hour 9: 2 engaged (on+active), 1 idle-on; hour 10: 1 MACRO (monitor off + input active).
	if _, _, err := st.InsertSamples(id, []shared.Sample{
		{ClientTS: h9 + 60, MonitorOn: 1, IsIdle: false, Exe: "game.exe"},
		{ClientTS: h9 + 120, MonitorOn: 1, IsIdle: false, Exe: "game.exe"},
		{ClientTS: h9 + 180, MonitorOn: 1, IsIdle: true, Exe: "game.exe"},
		{ClientTS: dayBase + 10*3600 + 60, MonitorOn: 0, IsIdle: false, Exe: "game.exe"},
	}, dayBase+11*3600); err != nil {
		t.Fatal(err)
	}
	day := DayUTC(dayBase)

	sig, err := st.DeviceSignals("dev-sig", dayStartUnix(day), dayBase+86400)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 1 {
		t.Fatalf("signals days = %d, want 1", len(sig))
	}
	s0 := sig[0]
	if s0.MonitorMinutes != 3 || s0.ActiveMinutes != 2 || s0.MacroMinutes != 1 || s0.SessionMinutes != 4 {
		t.Fatalf("signal day = %+v, want monitor=3 active=2 macro=1 session=4", s0)
	}

	hm, err := st.DeviceHeatmap("dev-sig", dayStartUnix(day), dayBase+86400)
	if err != nil {
		t.Fatal(err)
	}
	if len(hm) != 1 {
		t.Fatalf("heatmap days = %d, want 1", len(hm))
	}
	if hm[0].Hours[9] != 3 || hm[0].Hours[10] != 0 {
		t.Fatalf("heatmap hours 9/10 = %d/%d, want 3/0 (hour 10 was monitor-off)", hm[0].Hours[9], hm[0].Hours[10])
	}
}

func TestLogTitlesOptOut(t *testing.T) {
	st := openTestStore(t)
	id, err := st.UpsertDevice("dev-priv", "host", "1.0", dayBase)
	if err != nil {
		t.Fatal(err)
	}
	// Default: titles are logged.
	if on, _ := st.DeviceLogTitles(id); !on {
		t.Fatal("log_titles should default to true")
	}
	if err := st.SetLogTitles("dev-priv", false); err != nil {
		t.Fatal(err)
	}
	if on, _ := st.DeviceLogTitles(id); on {
		t.Fatal("log_titles should be false after opt-out")
	}
	// Status reflects the preference.
	d, err := st.DeviceStatusByUUID("dev-priv", dayBase)
	if err != nil {
		t.Fatal(err)
	}
	if d.LogTitles {
		t.Fatal("DeviceStatus.LogTitles should be false")
	}
}
