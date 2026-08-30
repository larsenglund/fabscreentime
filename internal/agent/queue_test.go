package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

// TestQueueRecoversFromCorruptFile is the regression for the offline-agents
// incident: an unclean shutdown left queue.json as NUL bytes, and the agent
// crashed fatally on every start trying to parse it. A corrupt buffer must never
// brick the agent — it recovers by starting empty and stays usable.
func TestQueueRecoversFromCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := os.WriteFile(path, []byte{0, 0, 0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}

	q, err := NewQueue(path, 100)
	if err != nil {
		t.Fatalf("NewQueue on a corrupt file must not error, got %v", err)
	}
	if q.Len() != 0 {
		t.Fatalf("corrupt queue should load empty, got %d", q.Len())
	}

	// Still fully usable afterwards, and the corruption is gone on reload.
	if err := q.Enqueue(shared.Sample{ClientTS: 1, MonitorOn: 1}); err != nil {
		t.Fatal(err)
	}
	q2, err := NewQueue(path, 100)
	if err != nil {
		t.Fatalf("reload after recovery errored: %v", err)
	}
	if q2.Len() != 1 {
		t.Fatalf("reload len = %d, want 1", q2.Len())
	}
}

// TestQueuePersistRoundTrips is a basic guard on the fsync-then-rename persist.
func TestQueuePersistRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	q, err := NewQueue(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := q.Enqueue(shared.Sample{ClientTS: int64(i), MonitorOn: 1}); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := NewQueue(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Len() != 3 {
		t.Fatalf("reloaded len = %d, want 3", reloaded.Len())
	}
}
