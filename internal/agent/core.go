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
	Build        int64 // monotonic build number, reported to the backend and used by the updater
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
	updater   Updater // may be nil (e.g. no pinned keys, or in tests)
	clockSkew time.Duration
}

// New wires an agent together. updater may be nil to disable self-update.
func New(cfg Config, s Sampler, q *Queue, u Uploader, up Updater) *Agent {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	return &Agent{cfg: cfg, sampler: s, queue: q, uploader: u, updater: up}
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
		AgentBuild:   a.cfg.Build,
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

	// Piggybacked update check (PLAN.md §5.1). The agent evaluates the signed
	// manifest whenever one is present — it does not trust the backend's
	// Available flag for the security decision (a compromised backend must not
	// be able to suppress a genuine, pinned-key-signed update via that flag).
	if a.updater != nil && resp.Update.Manifest != nil {
		a.updater.Maybe(ctx, *resp.Update.Manifest)
	}
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
