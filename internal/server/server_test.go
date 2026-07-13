package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func TestIngestThenSummary(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv, _ := newTestServer(t, now)
	h := srv.Handler()

	req := shared.IngestRequest{
		AgentVersion: "0.0.1",
		DeviceUUID:   "dev-abc",
		Hostname:     "living-room",
		Samples: []shared.Sample{
			{ClientTS: now.Unix() - 120, MonitorOn: 1, IsIdle: false, Exe: "game.exe", Title: "Game"},
			{ClientTS: now.Unix() - 60, MonitorOn: 1, IsIdle: true, Exe: "chrome.exe", Title: "Web"},
		},
	}
	rec := doIngest(t, h, req)
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
	rec2 := doIngest(t, h, req)
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

	req := shared.IngestRequest{
		DeviceUUID: "dev-x",
		Samples: []shared.Sample{
			{ClientTS: now.Unix(), MonitorOn: 1, Exe: "ok.exe"},           // in window
			{ClientTS: now.Unix() - 10*24*3600, MonitorOn: 1, Exe: "old"}, // too old → rejected
			{ClientTS: now.Unix() + 3600, MonitorOn: 1, Exe: "future"},    // too far ahead → rejected
		},
	}
	rec := doIngest(t, h, req)
	var ing shared.IngestResponse
	mustJSON(t, rec.Body.Bytes(), &ing)
	if ing.Accepted != 1 {
		t.Fatalf("accepted = %d, want 1 (2 rejected for bad ts)", ing.Accepted)
	}
}

func TestIngestRequiresDeviceUUID(t *testing.T) {
	srv, _ := newTestServer(t, time.Unix(1_700_000_000, 0))
	rec := doIngest(t, srv.Handler(), shared.IngestRequest{DeviceUUID: ""})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
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

func doIngest(t *testing.T, h http.Handler, req shared.IngestRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/api/ingest", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
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
