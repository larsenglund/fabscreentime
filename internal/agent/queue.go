package agent

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

// Queue is a bounded, file-backed FIFO of samples awaiting upload. It is
// drop-oldest when full so a long backend outage can't grow it without bound
// (PLAN.md §4.8). Persistence is a whole-file atomic rewrite — trivially cheap
// at this scale (a few thousand tiny rows at most).
type Queue struct {
	mu   sync.Mutex
	max  int
	path string
	buf  []shared.Sample
}

// NewQueue opens (or creates) a queue backed by path, holding at most max samples.
func NewQueue(path string, max int) (*Queue, error) {
	if max <= 0 {
		max = 10080 // ~7 days at 1/min
	}
	q := &Queue{path: path, max: max}
	if err := q.load(); err != nil {
		return nil, err
	}
	return q, nil
}

func (q *Queue) load() error {
	data, err := os.ReadFile(q.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, &q.buf)
}

func (q *Queue) persist() error {
	data, err := json.Marshal(q.buf)
	if err != nil {
		return err
	}
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, q.path)
}

// Enqueue appends a sample, dropping the oldest if over capacity.
func (q *Queue) Enqueue(s shared.Sample) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.buf = append(q.buf, s)
	if len(q.buf) > q.max {
		q.buf = append(q.buf[:0], q.buf[len(q.buf)-q.max:]...)
	}
	return q.persist()
}

// Snapshot returns a copy of all queued samples, oldest first.
func (q *Queue) Snapshot() []shared.Sample {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]shared.Sample, len(q.buf))
	copy(out, q.buf)
	return out
}

// Ack removes the first n samples — the ones just uploaded. Samples enqueued
// during the upload are at the tail and are preserved.
func (q *Queue) Ack(n int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if n >= len(q.buf) {
		q.buf = nil
	} else {
		q.buf = append(q.buf[:0], q.buf[n:]...)
	}
	return q.persist()
}

// Len reports the number of queued samples.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.buf)
}
