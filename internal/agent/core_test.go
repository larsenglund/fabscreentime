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
	fail     bool
	gotBatch int
	serverTS int64
}

func (f *fakeUploader) Upload(_ context.Context, req shared.IngestRequest) (shared.IngestResponse, error) {
	if f.fail {
		return shared.IngestResponse{}, errors.New("network down")
	}
	f.gotBatch = len(req.Samples)
	return shared.IngestResponse{Accepted: len(req.Samples), ServerTime: f.serverTS}, nil
}

func newTestAgent(t *testing.T, u Uploader) (*Agent, *Queue) {
	t.Helper()
	q, err := NewQueue(filepath.Join(t.TempDir(), "queue.json"), 5)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	fixedNow := time.Unix(1_700_000_000, 0)
	a := New(Config{
		DeviceUUID: "dev-1",
		Interval:   time.Minute,
		Now:        func() time.Time { return fixedNow },
	}, NewSampler(), q, u)
	return a, q
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
