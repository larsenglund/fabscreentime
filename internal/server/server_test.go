package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

func newTestServer(t *testing.T, now time.Time) (*Server, *Store) {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv := New(st, "")
	srv.now = func() time.Time { return now }
	return srv, st
}

// enrollDevice runs the real prepare→enroll handshake over the handler and
// returns the device UUID and its durable API token.
func enrollDevice(t *testing.T, h http.Handler, name string) (uuid, token string) {
	t.Helper()
	var prep shared.PrepareEnrollResponse
	rec := doJSON(t, h, http.MethodPost, "/api/enroll/prepare", "", shared.PrepareEnrollRequest{Name: name})
	if rec.Code != http.StatusOK {
		t.Fatalf("prepare status = %d, body=%s", rec.Code, rec.Body)
	}
	mustJSON(t, rec.Body.Bytes(), &prep)

	var enr shared.EnrollResponse
	rec = doJSON(t, h, http.MethodPost, "/api/enroll", "", shared.EnrollRequest{EnrollSecret: prep.EnrollSecret, Hostname: "host"})
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll status = %d, body=%s", rec.Code, rec.Body)
	}
	mustJSON(t, rec.Body.Bytes(), &enr)
	if enr.DeviceUUID != prep.DeviceUUID {
		t.Fatalf("enroll uuid %s != prepared %s", enr.DeviceUUID, prep.DeviceUUID)
	}
	return enr.DeviceUUID, enr.APIToken
}

func TestIngestThenSummary(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv, _ := newTestServer(t, now)
	h := srv.Handler()
	_, token := enrollDevice(t, h, "Living room PC")

	req := shared.IngestRequest{
		AgentVersion: "0.0.1",
		Hostname:     "living-room",
		Samples: []shared.Sample{
			{ClientTS: now.Unix() - 120, MonitorOn: 1, IsIdle: false, Exe: "game.exe", Title: "Game"},
			{ClientTS: now.Unix() - 60, MonitorOn: 1, IsIdle: true, Exe: "chrome.exe", Title: "Web"},
		},
	}
	rec := doIngest(t, h, token, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d, body=%s", rec.Code, rec.Body)
	}
	var ing shared.IngestResponse
	mustJSON(t, rec.Body.Bytes(), &ing)
	if ing.Accepted != 2 {
		t.Fatalf("accepted = %d, want 2", ing.Accepted)
	}
	if ing.ServerTime != now.Unix() {
		t.Fatalf("server_time = %d, want %d", ing.ServerTime, now.Unix())
	}

	// Re-send the same batch: idempotent, so zero new rows accepted.
	rec2 := doIngest(t, h, token, req)
	var ing2 shared.IngestResponse
	mustJSON(t, rec2.Body.Bytes(), &ing2)
	if ing2.Accepted != 0 {
		t.Fatalf("duplicate ingest accepted = %d, want 0", ing2.Accepted)
	}

	// Summary should reflect the two samples.
	sreq := httptest.NewRequest(http.MethodGet, "/api/dashboard/summary?range=7d", nil)
	srec := httptest.NewRecorder()
	h.ServeHTTP(srec, sreq)
	if srec.Code != http.StatusOK {
		t.Fatalf("summary status = %d", srec.Code)
	}
	var out struct {
		Devices []DeviceSummary `json:"devices"`
	}
	mustJSON(t, srec.Body.Bytes(), &out)
	if len(out.Devices) != 1 {
		t.Fatalf("want 1 device, got %d", len(out.Devices))
	}
	d := out.Devices[0]
	if d.MonitorMinutes != 2 || d.ActiveMinutes != 1 || d.SampleCount != 2 {
		t.Fatalf("summary got monitor=%d active=%d samples=%d, want 2/1/2",
			d.MonitorMinutes, d.ActiveMinutes, d.SampleCount)
	}
}

func TestIngestRejectsOutOfWindowTimestamps(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv, _ := newTestServer(t, now)
	h := srv.Handler()
	_, token := enrollDevice(t, h, "dev-x")

	req := shared.IngestRequest{
		Samples: []shared.Sample{
			{ClientTS: now.Unix(), MonitorOn: 1, Exe: "ok.exe"},           // in window
			{ClientTS: now.Unix() - 10*24*3600, MonitorOn: 1, Exe: "old"}, // too old → rejected
			{ClientTS: now.Unix() + 3600, MonitorOn: 1, Exe: "future"},    // too far ahead → rejected
		},
	}
	rec := doIngest(t, h, token, req)
	var ing shared.IngestResponse
	mustJSON(t, rec.Body.Bytes(), &ing)
	if ing.Accepted != 1 {
		t.Fatalf("accepted = %d, want 1 (2 rejected for bad ts)", ing.Accepted)
	}
}

func TestIngestRequiresToken(t *testing.T) {
	srv, _ := newTestServer(t, time.Unix(1_700_000_000, 0))
	h := srv.Handler()

	// No token → 401.
	if rec := doIngest(t, h, "", shared.IngestRequest{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", rec.Code)
	}
	// Garbage token → 401.
	if rec := doIngest(t, h, "not-a-real-token", shared.IngestRequest{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad-token status = %d, want 401", rec.Code)
	}
}

func TestEnrollmentIsSingleUse(t *testing.T) {
	srv, _ := newTestServer(t, time.Unix(1_700_000_000, 0))
	h := srv.Handler()

	var prep shared.PrepareEnrollResponse
	rec := doJSON(t, h, http.MethodPost, "/api/enroll/prepare", "", shared.PrepareEnrollRequest{Name: "PC"})
	mustJSON(t, rec.Body.Bytes(), &prep)

	// First use succeeds.
	rec = doJSON(t, h, http.MethodPost, "/api/enroll", "", shared.EnrollRequest{EnrollSecret: prep.EnrollSecret})
	if rec.Code != http.StatusOK {
		t.Fatalf("first enroll = %d, want 200", rec.Code)
	}
	// Reuse of the consumed secret is rejected.
	rec = doJSON(t, h, http.MethodPost, "/api/enroll", "", shared.EnrollRequest{EnrollSecret: prep.EnrollSecret})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("reused enroll = %d, want 401", rec.Code)
	}
}

func TestRevokedDeviceIsRejected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv, _ := newTestServer(t, now)
	h := srv.Handler()
	uuid, token := enrollDevice(t, h, "PC")

	// Works before revocation.
	if rec := doIngest(t, h, token, sample1(now)); rec.Code != http.StatusOK {
		t.Fatalf("pre-revoke ingest = %d, want 200", rec.Code)
	}
	// Revoke via the dashboard endpoint.
	revoked := true
	rec := doJSON(t, h, http.MethodPatch, "/api/devices/"+uuid, "", shared.PatchDeviceRequest{Revoked: &revoked})
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d, want 200", rec.Code)
	}
	// Its token now fails auth.
	if rec := doIngest(t, h, token, sample1(now)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("post-revoke ingest = %d, want 401", rec.Code)
	}
}

func TestEnrollPollReportsStatus(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv, _ := newTestServer(t, now)
	h := srv.Handler()

	var prep shared.PrepareEnrollResponse
	rec := doJSON(t, h, http.MethodPost, "/api/enroll/prepare", "", shared.PrepareEnrollRequest{Name: "PC"})
	mustJSON(t, rec.Body.Bytes(), &prep)

	// Before check-in: pending.
	rec = doJSON(t, h, http.MethodGet, "/api/devices/"+prep.DeviceUUID, "", nil)
	var d shared.DeviceStatus
	mustJSON(t, rec.Body.Bytes(), &d)
	if d.Status != "pending" {
		t.Fatalf("status = %q, want pending", d.Status)
	}

	// After enroll: active.
	doJSON(t, h, http.MethodPost, "/api/enroll", "", shared.EnrollRequest{EnrollSecret: prep.EnrollSecret})
	rec = doJSON(t, h, http.MethodGet, "/api/devices/"+prep.DeviceUUID, "", nil)
	mustJSON(t, rec.Body.Bytes(), &d)
	if d.Status != "active" {
		t.Fatalf("status = %q, want active", d.Status)
	}
}

func TestTitleOptOutStripsOnIngest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv, st := newTestServer(t, now)
	h := srv.Handler()
	uuid, token := enrollDevice(t, h, "PC")

	// Opt out of window titles via the dashboard.
	no := false
	rec := doJSON(t, h, http.MethodPatch, "/api/devices/"+uuid, "", shared.PatchDeviceRequest{LogTitles: &no})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch log_titles = %d, want 200", rec.Code)
	}

	// Ingest a sample carrying a sensitive title.
	doIngest(t, h, token, shared.IngestRequest{
		Samples: []shared.Sample{{ClientTS: now.Unix(), MonitorOn: 1, Exe: "chrome.exe", Title: "Online banking — Acme Bank"}},
	})

	// The stored title must be empty (dropped server-side), exe kept.
	var title, exe string
	err := st.db.QueryRow(`
		SELECT COALESCE(window_title,''), COALESCE(exe_name,'')
		FROM samples s JOIN devices d ON d.id = s.device_id WHERE d.device_uuid = ?`, uuid).Scan(&title, &exe)
	if err != nil {
		t.Fatal(err)
	}
	if title != "" {
		t.Fatalf("title = %q, want empty (opted out)", title)
	}
	if exe != "chrome.exe" {
		t.Fatalf("exe = %q, want chrome.exe (kept)", exe)
	}
}

func TestDeleteDeviceRemovesEverything(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv, st := newTestServer(t, now)
	h := srv.Handler()
	uuid, token := enrollDevice(t, h, "to-delete")

	// Ingest a sample + a transition so there is child data and a dirty rollup.
	doIngest(t, h, token, shared.IngestRequest{
		Samples: []shared.Sample{{ClientTS: now.Unix(), MonitorOn: 1, Exe: "a.exe"}},
		Events:  []shared.MonitorEvent{{ClientTS: now.Unix(), MonitorOn: 1}},
	})
	srv.mu.Lock()
	dirtyBefore := len(srv.dirty)
	srv.mu.Unlock()
	if dirtyBefore == 0 {
		t.Fatal("expected a dirty rollup entry after ingest")
	}

	// Delete it.
	if rec := doJSON(t, h, http.MethodDelete, "/api/devices/"+uuid, "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", rec.Code)
	}

	// Gone from the registry; its token no longer authenticates.
	if _, err := st.DeviceStatusByUUID(uuid, now.Unix()); err == nil {
		t.Fatal("device still present after delete")
	}
	if r := doIngest(t, h, token, sample1(now)); r.Code != http.StatusUnauthorized {
		t.Fatalf("post-delete ingest = %d, want 401", r.Code)
	}
	// Child data erased.
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM samples`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("samples remain after delete: %d", n)
	}
	// Dirty set purged, so the rollup ticker won't error-loop on the gone device.
	srv.mu.Lock()
	dirtyAfter := len(srv.dirty)
	srv.mu.Unlock()
	if dirtyAfter != 0 {
		t.Fatalf("dirty not purged: %d", dirtyAfter)
	}
	srv.RunRollups() // must be a no-op, not an FK error loop

	// Deleting an unknown UUID → 404.
	if r := doJSON(t, h, http.MethodDelete, "/api/devices/does-not-exist", "", nil); r.Code != http.StatusNotFound {
		t.Fatalf("delete unknown = %d, want 404", r.Code)
	}
}

// TestBehindLatestFlag covers the §5.3 M4 "update pending" path: the agent's
// build is stored on ingest and echoed in the device status, and /api/devices
// reports the latest published release so the dashboard can flag laggards.
func TestBehindLatestFlag(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	// A server that serves a signed release at build 7.
	agentDir := t.TempDir()
	manifest := shared.SignedManifest{Manifest: shared.Manifest{Version: "0.7.0", Build: 7}}
	b, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(agentDir, "manifest.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv := New(st, agentDir)
	srv.now = func() time.Time { return now }
	h := srv.Handler()

	uuid, token := enrollDevice(t, h, "old-agent")
	// Ingest from an agent still on build 5 — behind the published build 7.
	if rec := doIngest(t, h, token, shared.IngestRequest{
		AgentVersion: "0.5.0",
		AgentBuild:   5,
		Samples:      []shared.Sample{{ClientTS: now.Unix(), MonitorOn: 1, Exe: "a.exe"}},
	}); rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d", rec.Code)
	}

	// The device status carries the reported build.
	var d shared.DeviceStatus
	rec := doJSON(t, h, http.MethodGet, "/api/devices/"+uuid, "", nil)
	mustJSON(t, rec.Body.Bytes(), &d)
	if d.AgentBuild != 5 {
		t.Fatalf("agent_build = %d, want 5", d.AgentBuild)
	}

	// The devices list exposes the latest release, so the client can compare.
	rec = doJSON(t, h, http.MethodGet, "/api/devices", "", nil)
	var list struct {
		Devices []shared.DeviceStatus `json:"devices"`
		Latest  *shared.LatestAgent   `json:"latest"`
	}
	mustJSON(t, rec.Body.Bytes(), &list)
	if list.Latest == nil || list.Latest.Build != 7 {
		t.Fatalf("latest = %+v, want build 7", list.Latest)
	}
	if len(list.Devices) != 1 || list.Devices[0].AgentBuild >= list.Latest.Build {
		t.Fatalf("device should be behind latest: dev=%+v latest=%+v", list.Devices, list.Latest)
	}
}

// TestClockSkewObservedOnIngest covers the §8 clock-skew flag: an agent that
// reports a wall-clock ahead of the server records a positive skew on its status;
// an agent that omits client_now leaves the last-known skew untouched.
func TestClockSkewObservedOnIngest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv, _ := newTestServer(t, now)
	h := srv.Handler()
	uuid, token := enrollDevice(t, h, "skewed")

	// Fresh device: skew unknown until the agent reports its clock.
	var d shared.DeviceStatus
	mustJSON(t, doJSON(t, h, http.MethodGet, "/api/devices/"+uuid, "", nil).Body.Bytes(), &d)
	if d.ClockSkewKnown {
		t.Fatalf("clock skew should be unknown before any client_now is reported")
	}

	// Agent clock is 300s ahead of the server.
	doIngest(t, h, token, shared.IngestRequest{
		ClientNow: now.Unix() + 300,
		Samples:   []shared.Sample{{ClientTS: now.Unix(), MonitorOn: 1, Exe: "a.exe"}},
	})
	mustJSON(t, doJSON(t, h, http.MethodGet, "/api/devices/"+uuid, "", nil).Body.Bytes(), &d)
	if !d.ClockSkewKnown || d.ClockSkew != 300 {
		t.Fatalf("clock_skew = %d (known=%v), want +300 known", d.ClockSkew, d.ClockSkewKnown)
	}

	// A later ingest without client_now (older agent) must not blank the skew.
	doIngest(t, h, token, shared.IngestRequest{
		Samples: []shared.Sample{{ClientTS: now.Unix() + 1, MonitorOn: 1, Exe: "a.exe"}},
	})
	mustJSON(t, doJSON(t, h, http.MethodGet, "/api/devices/"+uuid, "", nil).Body.Bytes(), &d)
	if !d.ClockSkewKnown || d.ClockSkew != 300 {
		t.Fatalf("clock_skew after skewless ingest = %d (known=%v), want +300 retained", d.ClockSkew, d.ClockSkewKnown)
	}
}

// TestSelfUpdateAuditLog covers §8 tamper-evidence: a build change on ingest is
// recorded as an "updated" event, a backwards move as a "downgrade", and the
// first-ever check-in logs nothing.
func TestSelfUpdateAuditLog(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv, _ := newTestServer(t, now)
	h := srv.Handler()
	uuid, token := enrollDevice(t, h, "updater")

	ingestBuild := func(ver string, build int64) {
		doIngest(t, h, token, shared.IngestRequest{
			AgentVersion: ver, AgentBuild: build,
			Samples: []shared.Sample{{ClientTS: now.Unix(), MonitorOn: 1, Exe: "a.exe"}},
		})
	}
	events := func() []shared.DeviceEvent {
		var out struct {
			Events []shared.DeviceEvent `json:"events"`
		}
		mustJSON(t, doJSON(t, h, http.MethodGet, "/api/devices/"+uuid+"/events", "", nil).Body.Bytes(), &out)
		return out.Events
	}

	ingestBuild("0.6.0", 5) // first check-in: no prior build → no event
	if len(events()) != 0 {
		t.Fatalf("first check-in should log no event, got %d", len(events()))
	}
	ingestBuild("0.6.0", 5) // same build → no event
	ingestBuild("0.7.0", 6) // forward → "updated"
	ingestBuild("0.5.0", 4) // backward → "downgrade"

	ev := events()
	if len(ev) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(ev), ev)
	}
	// Newest first: the downgrade, then the update.
	if ev[0].Kind != "downgrade" || ev[1].Kind != "updated" {
		t.Fatalf("event kinds = [%s, %s], want [downgrade, updated]", ev[0].Kind, ev[1].Kind)
	}
	if !strings.Contains(ev[1].Detail, "build 5") || !strings.Contains(ev[1].Detail, "build 6") {
		t.Fatalf("update detail = %q, want the 5→6 transition", ev[1].Detail)
	}
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t, time.Now())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func sample1(now time.Time) shared.IngestRequest {
	return shared.IngestRequest{Samples: []shared.Sample{{ClientTS: now.Unix(), MonitorOn: 1, Exe: "a.exe"}}}
}

func doIngest(t *testing.T, h http.Handler, token string, req shared.IngestRequest) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, h, http.MethodPost, "/api/ingest", token, req)
}

// doJSON issues a JSON request, optionally with a bearer token, and returns the
// recorder. A nil body sends no payload.
func doJSON(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func mustJSON(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("json: %v (%s)", err, data)
	}
}
