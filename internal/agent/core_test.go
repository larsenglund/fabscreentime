package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

type fakeUploader struct {
	fail       bool
	gotBatch   int
	gotEvents  int
	gotBuild   int64
	serverTS   int64
	updateSM   *shared.SignedManifest
	updateFlag bool
}

func (f *fakeUploader) Upload(_ context.Context, req shared.IngestRequest) (shared.IngestResponse, error) {
	if f.fail {
		return shared.IngestResponse{}, errors.New("network down")
	}
	f.gotBatch = len(req.Samples)
	f.gotEvents = len(req.Events)
	f.gotBuild = req.AgentBuild
	return shared.IngestResponse{
		Accepted:   len(req.Samples),
		ServerTime: f.serverTS,
		Update:     shared.UpdateInfo{Available: f.updateFlag, Manifest: f.updateSM},
	}, nil
}

func newTestAgent(t *testing.T, u Uploader) (*Agent, *Queue) {
	a, q, _ := newTestAgentWithUpdater(t, u, nil)
	return a, q
}

func newTestAgentWithUpdater(t *testing.T, u Uploader, up Updater) (*Agent, *Queue, Config) {
	t.Helper()
	q, err := NewQueue(filepath.Join(t.TempDir(), "queue.json"), 5)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	fixedNow := time.Unix(1_700_000_000, 0)
	cfg := Config{
		DeviceUUID: "dev-1",
		Build:      3,
		Interval:   time.Minute,
		Now:        func() time.Time { return fixedNow },
	}
	return New(cfg, NewSampler(), q, u, up), q, cfg
}

type fakeUpdater struct {
	calls  int
	lastSM shared.SignedManifest
}

func (f *fakeUpdater) Maybe(_ context.Context, sm shared.SignedManifest) {
	f.calls++
	f.lastSM = sm
}

func TestTickRetainsQueueWhenUploadFails(t *testing.T) {
	up := &fakeUploader{fail: true}
	a, q := newTestAgent(t, up)
	a.Tick(context.Background())
	if q.Len() != 1 {
		t.Fatalf("want 1 sample retained after failed upload, got %d", q.Len())
	}
	// Recover: next tick should upload both the retained and the new sample.
	up.fail = false
	a.Tick(context.Background())
	if q.Len() != 0 {
		t.Fatalf("want queue drained after successful upload, got %d", q.Len())
	}
	if up.gotBatch != 2 {
		t.Fatalf("want batch of 2 (backlog flushed together), got %d", up.gotBatch)
	}
}

func TestQueueDropsOldestWhenFull(t *testing.T) {
	up := &fakeUploader{fail: true} // never drains
	a, q := newTestAgent(t, up)
	for i := 0; i < 8; i++ {
		a.Tick(context.Background())
	}
	if q.Len() != 5 {
		t.Fatalf("want cap of 5 enforced, got %d", q.Len())
	}
}

func TestClockSkewComputedFromServerTime(t *testing.T) {
	// Agent clock is fixed at 1_700_000_000; server says it's 90s ahead.
	up := &fakeUploader{serverTS: 1_700_000_090}
	a, _ := newTestAgent(t, up)
	a.Tick(context.Background())
	if got := a.ClockSkew(); got != 90*time.Second {
		t.Fatalf("want +90s skew, got %s", got)
	}
}

func TestQueuePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")
	q1, _ := NewQueue(path, 100)
	_ = q1.Enqueue(shared.Sample{ClientTS: 1, Exe: "a.exe"})
	_ = q1.Enqueue(shared.Sample{ClientTS: 2, Exe: "b.exe"})

	q2, err := NewQueue(path, 100)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if q2.Len() != 2 {
		t.Fatalf("want 2 samples reloaded from disk, got %d", q2.Len())
	}
}

func TestUpdaterInvokedOnlyWhenManifestPresent(t *testing.T) {
	// No manifest → updater not called.
	up := &fakeUpdater{}
	upl := &fakeUploader{}
	a, _, _ := newTestAgentWithUpdater(t, upl, up)
	a.Tick(context.Background())
	if up.calls != 0 {
		t.Fatalf("updater called %d times with no manifest, want 0", up.calls)
	}
	if upl.gotBuild != 3 {
		t.Fatalf("agent build reported as %d, want 3", upl.gotBuild)
	}

	// Manifest present → updater called with it, regardless of Available.
	upl.updateSM = &shared.SignedManifest{Manifest: shared.Manifest{Version: "9.9.9", Build: 99}}
	upl.updateFlag = false
	a.Tick(context.Background())
	if up.calls != 1 {
		t.Fatalf("updater called %d times with manifest present, want 1", up.calls)
	}
	if up.lastSM.Manifest.Version != "9.9.9" {
		t.Fatalf("updater got manifest %q, want 9.9.9", up.lastSM.Manifest.Version)
	}
}

func TestMonitorTransitionsDedupedBufferedAndFlushed(t *testing.T) {
	up := &fakeUploader{}
	a, _, _ := newTestAgentWithUpdater(t, up, nil)

	a.recordTransition(1, 100) // seed off(-1)→on: event
	a.recordTransition(1, 160) // no change: deduped
	a.recordTransition(0, 200) // on→off: event
	if got := len(a.snapshotEvents()); got != 2 {
		t.Fatalf("buffered events = %d, want 2 (deduped)", got)
	}

	a.Tick(context.Background()) // successful flush uploads samples + events
	if up.gotEvents != 2 {
		t.Fatalf("uploaded events = %d, want 2", up.gotEvents)
	}
	if got := len(a.snapshotEvents()); got != 0 {
		t.Fatalf("events not cleared after successful flush: %d", got)
	}
}

func TestMonitorEventsRetainedOnUploadFailure(t *testing.T) {
	up := &fakeUploader{fail: true}
	a, _, _ := newTestAgentWithUpdater(t, up, nil)
	a.recordTransition(0, 100)
	a.Tick(context.Background()) // upload fails
	if got := len(a.snapshotEvents()); got != 1 {
		t.Fatalf("events lost on failed upload: have %d, want 1 retained", got)
	}
}

func TestDeriveMonitorOn(t *testing.T) {
	cases := []struct {
		name string
		r    Reading
		want int
	}{
		{"unknown-assumes-on", Reading{MonitorsActive: -1, DisplayPower: -1}, 1},
		{"connected-and-powered", Reading{MonitorsActive: 2, DisplayPower: 1}, 1},
		{"disconnected", Reading{MonitorsActive: 0, DisplayPower: 1}, 0},
		{"powered-off", Reading{MonitorsActive: 1, DisplayPower: 0}, 0},
	}
	for _, c := range cases {
		if got := deriveMonitorOn(c.r); got != c.want {
			t.Errorf("%s: deriveMonitorOn=%d want %d", c.name, got, c.want)
		}
	}
}
