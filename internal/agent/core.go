package agent

import (
	"context"
	"log"
	"time"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

// Uploader sends a batch to the backend and returns the parsed response.
// Abstracted so the core can be tested without a real HTTP server.
type Uploader interface {
	Upload(ctx context.Context, req shared.IngestRequest) (shared.IngestResponse, error)
}

// Config holds the agent's runtime parameters.
type Config struct {
	DeviceUUID   string
	Hostname     string
	AgentVersion string
	Interval     time.Duration
	Now          func() time.Time // injectable for tests
}

// Agent samples once per interval, buffers to a bounded queue, and flushes the
// whole backlog to the backend on each cycle (PLAN.md §4).
type Agent struct {
	cfg       Config
	sampler   Sampler
	queue     *Queue
	uploader  Uploader
	clockSkew time.Duration
}

// New wires an agent together.
func New(cfg Config, s Sampler, q *Queue, u Uploader) *Agent {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	return &Agent{cfg: cfg, sampler: s, queue: q, uploader: u}
}

// deriveMonitorOn collapses the two raw monitor signals into a 0/1 the way the
// dashboard's headline metric reads them. When monitor state is unknown (the
// Phase 0 agent doesn't sample it) we optimistically report "on" so sessions
// still count; the raw -1s are preserved for later recomputation.
func deriveMonitorOn(r Reading) int {
	if r.MonitorsActive < 0 && r.DisplayPower < 0 {
		return 1
	}
	if r.MonitorsActive != 0 && r.DisplayPower != 0 {
		return 1
	}
	return 0
}

// Tick performs one sample → enqueue → flush cycle. Exported for tests.
func (a *Agent) Tick(ctx context.Context) {
	if r, err := a.sampler.Sample(); err != nil {
		log.Printf("sample error: %v", err)
	} else {
		s := shared.Sample{
			ClientTS:       a.cfg.Now().Unix(),
			MonitorsActive: r.MonitorsActive,
			DisplayPower:   r.DisplayPower,
			MonitorOn:      deriveMonitorOn(r),
			IsIdle:         r.IsIdle,
			IdleMS:         r.IdleMS,
			Exe:            r.ExeName,
			Title:          r.WindowTitle,
		}
		if err := a.queue.Enqueue(s); err != nil {
			log.Printf("enqueue error: %v", err)
		}
	}
	a.flush(ctx)
}

func (a *Agent) flush(ctx context.Context) {
	batch := a.queue.Snapshot()
	if len(batch) == 0 {
		return
	}
	resp, err := a.uploader.Upload(ctx, shared.IngestRequest{
		AgentVersion: a.cfg.AgentVersion,
		DeviceUUID:   a.cfg.DeviceUUID,
		Hostname:     a.cfg.Hostname,
		Samples:      batch,
	})
	if err != nil {
		log.Printf("upload failed (%d queued): %v", len(batch), err)
		return
	}
	if err := a.queue.Ack(len(batch)); err != nil {
		log.Printf("ack error: %v", err)
	}
	if resp.ServerTime > 0 {
		a.clockSkew = time.Duration(resp.ServerTime-a.cfg.Now().Unix()) * time.Second
	}
	log.Printf("uploaded %d samples (accepted %d, queued now %d, clock skew %s)",
		len(batch), resp.Accepted, a.queue.Len(), a.clockSkew)
}

// ClockSkew returns the last observed offset between server and local clocks.
func (a *Agent) ClockSkew() time.Duration { return a.clockSkew }

// Run samples immediately, then every interval until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) {
	a.Tick(ctx)
	t := time.NewTicker(a.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.Tick(ctx)
		}
	}
}
