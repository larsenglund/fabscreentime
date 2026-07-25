package agent

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

const maxPendingEvents = 4096 // bounded so a long outage can't grow it unbounded

// Uploader sends a batch to the backend and returns the parsed response.
// Abstracted so the core can be tested without a real HTTP server.
type Uploader interface {
	Upload(ctx context.Context, req shared.IngestRequest) (shared.IngestResponse, error)
}

// MonitorProber reports the composite monitor-on state (1 on, 0 off) cheaply, so
// the core can poll it faster than the 1/min sample cadence and emit precise
// on/off transition events (PLAN.md §4.5). Optional: only Windows implements it.
type MonitorProber interface {
	MonitorOn() int
}

// Config holds the agent's runtime parameters.
type Config struct {
	DeviceUUID   string
	Hostname     string
	AgentVersion string
	Build        int64 // monotonic build number, reported to the backend and used by the updater
	Interval     time.Duration
	MonitorPoll  time.Duration    // how often to poll for monitor on/off transitions
	Now          func() time.Time // injectable for tests
	// OnFirstCheckIn fires once after the first successful upload — used to commit
	// a freshly-updated build so a later restart isn't mistaken for a crash (§5.4).
	OnFirstCheckIn func()
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

	mu            sync.Mutex
	pendingEvents []shared.MonitorEvent
	lastMonitorOn int // -1 unknown, else last observed composite state
	checkedIn     bool
}

// New wires an agent together. updater may be nil to disable self-update.
func New(cfg Config, s Sampler, q *Queue, u Uploader, up Updater) *Agent {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.MonitorPoll <= 0 {
		cfg.MonitorPoll = 10 * time.Second
	}
	return &Agent{cfg: cfg, sampler: s, queue: q, uploader: u, updater: up, lastMonitorOn: -1}
}

// recordTransition appends a monitor on/off event if the composite state changed.
func (a *Agent) recordTransition(on int, ts int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if on == a.lastMonitorOn {
		return
	}
	a.lastMonitorOn = on
	a.pendingEvents = append(a.pendingEvents, shared.MonitorEvent{ClientTS: ts, MonitorOn: on})
	if len(a.pendingEvents) > maxPendingEvents {
		a.pendingEvents = append(a.pendingEvents[:0], a.pendingEvents[len(a.pendingEvents)-maxPendingEvents:]...)
	}
}

func (a *Agent) snapshotEvents() []shared.MonitorEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]shared.MonitorEvent, len(a.pendingEvents))
	copy(out, a.pendingEvents)
	return out
}

func (a *Agent) ackEvents(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n >= len(a.pendingEvents) {
		a.pendingEvents = nil
	} else {
		a.pendingEvents = append(a.pendingEvents[:0], a.pendingEvents[n:]...)
	}
}

// monitorPollLoop polls the composite monitor state and records transitions.
func (a *Agent) monitorPollLoop(ctx context.Context, p MonitorProber) {
	t := time.NewTicker(a.cfg.MonitorPoll)
	defer t.Stop()
	a.recordTransition(p.MonitorOn(), a.cfg.Now().Unix()) // seed
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.recordTransition(p.MonitorOn(), a.cfg.Now().Unix())
		}
	}
}

// ddcSaysOff interprets the raw DDC probe value per the rule validated in
// MONTEST-RESULTS.md: a VCP standby/off reply (2..5, the HDMI signature) is
// direct testimony; a missing physical-monitor handle (-1, the DP signature)
// is trusted only once armed — i.e. once DDC has provably worked this session
// — so hardware where DDC never works (VMs, RDP) fails toward "on", which is
// visible over-counting rather than silently zeroed screentime.
func ddcSaysOff(power int, armed bool) bool {
	switch {
	case power >= 2 && power <= 5:
		return true
	case power == DDCNoHandle:
		return armed
	default: // 1 = on; -2/-3/0 = no testimony
		return false
	}
}

// deriveMonitorOn collapses the raw monitor signals into a 0/1 the way the
// dashboard's headline metric reads them. Any signal testifying "off" wins;
// when everything is unknown we optimistically report "on" so sessions still
// count. The raw values are preserved for later recomputation server-side.
func deriveMonitorOn(r Reading) int {
	if ddcSaysOff(r.DDCPower, r.DDCArmed) {
		return 0
	}
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
			DDCPower:       r.DDCPower,
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
	events := a.snapshotEvents()
	if len(batch) == 0 && len(events) == 0 {
		return
	}
	resp, err := a.uploader.Upload(ctx, shared.IngestRequest{
		AgentVersion: a.cfg.AgentVersion,
		AgentBuild:   a.cfg.Build,
		DeviceUUID:   a.cfg.DeviceUUID,
		Hostname:     a.cfg.Hostname,
		ClientNow:    a.cfg.Now().Unix(), // for server-side clock-skew detection (§8)
		Samples:      batch,
		Events:       events,
	})
	if err != nil {
		log.Printf("upload failed (%d samples, %d events queued): %v", len(batch), len(events), err)
		return
	}
	if err := a.queue.Ack(len(batch)); err != nil {
		log.Printf("ack error: %v", err)
	}
	a.ackEvents(len(events))

	// First successful check-in commits this build (crash-loop guard, §5.4).
	if !a.checkedIn {
		a.checkedIn = true
		if a.cfg.OnFirstCheckIn != nil {
			a.cfg.OnFirstCheckIn()
		}
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

// Run samples immediately, then every interval until ctx is cancelled. If the
// sampler can probe monitor state, a faster poll loop records on/off transitions.
func (a *Agent) Run(ctx context.Context) {
	if p, ok := a.sampler.(MonitorProber); ok {
		go a.monitorPollLoop(ctx, p)
	}
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
