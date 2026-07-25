package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/larsenglund/fabscreentime/internal/shared"
	"github.com/larsenglund/fabscreentime/web"
)

const (
	maxIngestBytes = 1 << 20 // 1 MiB body cap (PLAN.md §6.3)
	maxEnrollBytes = 8 << 10 // 8 KiB is ample for an enrollment body
	enrollTTL      = 15 * 60 // one-time enrollment secret lifetime (seconds)
)

type dirtyKey struct {
	deviceID int64
	day      string
}

// Server holds the HTTP handlers and their dependencies.
type Server struct {
	store    *Store
	now      func() time.Time
	agentDir string // holds manifest.json + agent.exe (may be empty)

	enrollLimiter *rateLimiter // per-IP cap on the public enrollment endpoint

	distFS fs.FS        // embedded SPA build (may be nil if the embed failed)
	spa    http.Handler // static file server over distFS

	notifier     Notifier      // offline-device alerts (nil = disabled)
	offlineAfter time.Duration // silence before a device is called offline

	mu          sync.Mutex
	dirty       map[dirtyKey]bool      // (device, day) pairs whose rollup is stale
	manifest    *shared.SignedManifest // current signed release (hot-reloaded on mtime change)
	manifestMod time.Time              // mtime of the manifest last loaded
	alerted     map[string]bool        // device UUIDs currently in the "offline" alert state
	alertSeeded bool                   // first offline pass done (seed, don't alert)
}

// New returns a Server backed by store. agentDir, if non-empty, is scanned for a
// signed agent release (manifest.json + agent.exe) to serve for auto-update.
func New(store *Store, agentDir string) *Server {
	s := &Server{
		store:         store,
		now:           time.Now,
		agentDir:      agentDir,
		dirty:         map[dirtyKey]bool{},
		alerted:       map[string]bool{},
		offlineAfter:  15 * time.Minute,
		enrollLimiter: newRateLimiter(10, 60), // 10 enroll attempts / minute / IP
	}
	if m := s.currentManifest(); m != nil {
		log.Printf("serving agent release %s (build %d)", m.Manifest.Version, m.Manifest.Build)
	}
	if dfs, err := web.DistFS(); err != nil {
		log.Printf("embedded dashboard unavailable: %v", err)
	} else {
		s.distFS = dfs
		s.spa = http.FileServerFS(dfs)
	}
	return s
}

// currentManifest returns the signed release to serve, reloading it whenever the
// on-disk manifest.json changes. Unlike the backend binary (never auto-updated),
// the *agent* release is re-signed often during development, so hot-reloading on
// mtime means a fresh `release-local` is picked up without a backend restart.
func (s *Server) currentManifest() *shared.SignedManifest {
	if s.agentDir == "" {
		return nil
	}
	fi, err := os.Stat(filepath.Join(s.agentDir, "manifest.json"))
	if err != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.manifest // keep last-known on a transient stat error
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !fi.ModTime().After(s.manifestMod) {
		return s.manifest
	}
	data, err := os.ReadFile(filepath.Join(s.agentDir, "manifest.json"))
	if err != nil {
		return s.manifest
	}
	var sm shared.SignedManifest
	if err := json.Unmarshal(data, &sm); err != nil {
		log.Printf("agent manifest parse: %v", err)
		return s.manifest
	}
	if s.manifest != nil && sm.Manifest.Build != s.manifest.Manifest.Build {
		log.Printf("reloaded agent release %s (build %d)", sm.Manifest.Version, sm.Manifest.Build)
	}
	s.manifest = &sm
	s.manifestMod = fi.ModTime()
	return s.manifest
}

// Handler builds the request router (Go 1.22+ method+path patterns, no framework).
//
// Two auth planes (PLAN.md §6.3/§7.4): agent endpoints authenticate with a
// per-device bearer token (ingest) or a one-time secret (enroll) and stay
// public; the dashboard endpoints (summary, device management, enroll/prepare)
// carry no device auth and sit behind Cloudflare Access in production.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Agent plane (public).
	mux.HandleFunc("POST /api/ingest", s.handleIngest)
	mux.HandleFunc("POST /api/enroll", s.handleEnroll)
	mux.HandleFunc("GET /agent/manifest", s.handleManifest)
	mux.HandleFunc("GET /agent/download", s.handleDownload)
	// Dashboard plane (Cloudflare Access in prod).
	mux.HandleFunc("POST /api/enroll/prepare", s.handlePrepareEnroll)
	mux.HandleFunc("GET /api/dashboard/summary", s.handleSummary)
	mux.HandleFunc("GET /api/stats/trend", s.handleTrend)
	mux.HandleFunc("GET /api/devices", s.handleDevices)
	mux.HandleFunc("GET /api/devices/{uuid}", s.handleDevice)
	mux.HandleFunc("PATCH /api/devices/{uuid}", s.handleDevicePatch)
	mux.HandleFunc("DELETE /api/devices/{uuid}", s.handleDeleteDevice)
	mux.HandleFunc("GET /api/devices/{uuid}/timeline", s.handleTimeline)
	mux.HandleFunc("GET /api/devices/{uuid}/top-apps", s.handleTopApps)
	mux.HandleFunc("GET /api/devices/{uuid}/signals", s.handleSignals)
	mux.HandleFunc("GET /api/devices/{uuid}/heatmap", s.handleHeatmap)
	// Infra.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	// Everything else is the embedded SPA (static assets + client-side routes).
	mux.HandleFunc("GET /", s.handleSPA)
	return logRequests(mux)
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	id, err := s.store.DeviceByToken(token)
	if err != nil {
		// Unknown and revoked tokens are indistinguishable to the caller (401).
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxIngestBytes)
	var req shared.IngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	now := s.now().Unix()
	if err := s.store.TouchDevice(id, req.Hostname, req.AgentVersion, now); err != nil {
		log.Printf("touch device: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	// Privacy opt-out (§9): drop window titles server-side for devices with
	// log_titles=0, so they are never stored regardless of what the agent sends.
	if keep, err := s.store.DeviceLogTitles(id); err == nil && !keep {
		for i := range req.Samples {
			req.Samples[i].Title = ""
		}
	}
	accepted, sampleDays, err := s.store.InsertSamples(id, req.Samples, now)
	if err != nil {
		log.Printf("insert samples: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	_, eventDays, err := s.store.InsertMonitorEvents(id, req.Events, now)
	if err != nil {
		log.Printf("insert events: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	s.markDirty(id, sampleDays, eventDays)

	writeJSON(w, http.StatusOK, shared.IngestResponse{
		Accepted:   accepted,
		ServerTime: now,
		Update:     s.updateFor(req.AgentBuild),
	})
}

// markDirty records (device, day) pairs whose daily rollup needs recomputing.
// Tracking exactly which days each ingest touched — including a late backlog
// dated days ago — is what stops the rollup from silently dropping those minutes
// (PLAN.md §6.3, "dirty days").
func (s *Server) markDirty(deviceID int64, daySets ...map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, set := range daySets {
		for day := range set {
			s.dirty[dirtyKey{deviceID, day}] = true
		}
	}
}

// RunRollups recomputes every dirty (device, day) rollup and clears the set.
func (s *Server) RunRollups() {
	s.mu.Lock()
	pending := s.dirty
	s.dirty = map[dirtyKey]bool{}
	s.mu.Unlock()

	now := s.now().Unix()
	for k := range pending {
		if err := s.store.RollupDay(k.deviceID, k.day, now); err != nil {
			log.Printf("rollup device=%d day=%s: %v", k.deviceID, k.day, err)
			// Re-mark so a transient failure is retried next cycle.
			s.mu.Lock()
			s.dirty[k] = true
			s.mu.Unlock()
		}
	}
}

// CatchUpRollups re-marks every (device, day) with samples in the last `days`
// days and rolls them up now. The dirty set is in-memory, so without this a
// restart between ingest and the next rollup tick would lose those days from
// daily_stats permanently. Called once at startup; the rollup is idempotent.
func (s *Server) CatchUpRollups(days int) {
	since := s.now().AddDate(0, 0, -days).Unix()
	pairs, err := s.store.RecentSampleDays(since)
	if err != nil {
		log.Printf("rollup catch-up: %v", err)
		return
	}
	if len(pairs) == 0 {
		return
	}
	s.mu.Lock()
	for _, p := range pairs {
		s.dirty[dirtyKey{p.DeviceID, p.Day}] = true
	}
	s.mu.Unlock()
	log.Printf("rollup catch-up: recomputing %d device-days", len(pairs))
	s.RunRollups()
}

// EnableAlerts turns on offline-device push alerts to an ntfy-compatible webhook.
// A zero offlineAfter keeps the default. No-op notifier when webhookURL is empty.
func (s *Server) EnableAlerts(webhookURL string, offlineAfter time.Duration) {
	if webhookURL != "" {
		s.notifier = newNtfyNotifier(webhookURL)
	}
	if offlineAfter > 0 {
		s.offlineAfter = offlineAfter
	}
}

// StartRollupLoop runs RunRollups on interval until ctx is cancelled.
func (s *Server) StartRollupLoop(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.RunRollups()
			}
		}
	}()
}

// updateFor returns the signed update block. The agent re-verifies the manifest
// against its pinned keys regardless of Available, so this is only a hint.
func (s *Server) updateFor(agentBuild int64) shared.UpdateInfo {
	m := s.currentManifest()
	if m == nil {
		return shared.UpdateInfo{Available: false}
	}
	return shared.UpdateInfo{
		Available: m.Manifest.Build > agentBuild,
		Manifest:  m,
	}
}

func (s *Server) handleManifest(w http.ResponseWriter, _ *http.Request) {
	m := s.currentManifest()
	if m == nil {
		http.Error(w, "no release", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleEnroll is the public first-contact endpoint: a new agent exchanges its
// one-time secret for a durable API token. Rate-limited on the true client IP
// (the enrollment secret is the dangerous credential — PLAN.md §9).
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	ip := trueClientIP(r)
	if !s.enrollLimiter.allow(ip, s.now().Unix()) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEnrollBytes)
	var req shared.EnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.EnrollSecret) == "" {
		http.Error(w, "enroll_secret required", http.StatusBadRequest)
		return
	}
	uuid, apiToken, err := s.store.ConsumeEnrollment(req.EnrollSecret, req.Hostname, ip, s.now().Unix())
	if errors.Is(err, ErrEnrollInvalid) {
		http.Error(w, "enrollment rejected", http.StatusUnauthorized)
		return
	}
	if err != nil {
		log.Printf("enroll: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	log.Printf("enrolled device %s from %s (host %q)", uuid, ip, req.Hostname)
	writeJSON(w, http.StatusOK, shared.EnrollResponse{
		DeviceUUID:      uuid,
		APIToken:        apiToken,
		IngestIntervalS: 60,
	})
}

// handlePrepareEnroll mints a one-time enrollment secret bound to a new pending
// device (dashboard-side — behind Cloudflare Access in production).
func (s *Server) handlePrepareEnroll(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxEnrollBytes)
	var req shared.PrepareEnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "New device"
	}
	now := s.now().Unix()
	uuid, secret, err := s.store.PrepareEnrollment(name, now, now+enrollTTL)
	if err != nil {
		log.Printf("prepare enroll: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, shared.PrepareEnrollResponse{
		DeviceUUID:   uuid,
		EnrollSecret: secret,
		ExpiresIn:    enrollTTL,
	})
}

func (s *Server) handleDevices(w http.ResponseWriter, _ *http.Request) {
	list, err := s.store.DeviceStatuses(s.now().Unix())
	if err != nil {
		log.Printf("devices: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []shared.DeviceStatus{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": list})
}

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.DeviceStatusByUUID(r.PathValue("uuid"), s.now().Unix())
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDevicePatch(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	if _, err := s.store.DeviceStatusByUUID(uuid, s.now().Unix()); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEnrollBytes)
	var req shared.PatchDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Name != nil {
		if err := s.store.RenameDevice(uuid, strings.TrimSpace(*req.Name)); err != nil {
			log.Printf("rename device: %v", err)
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
	}
	if req.Revoked != nil {
		if err := s.store.SetRevoked(uuid, *req.Revoked); err != nil {
			log.Printf("revoke device: %v", err)
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
	}
	if req.LogTitles != nil {
		if err := s.store.SetLogTitles(uuid, *req.LogTitles); err != nil {
			log.Printf("set log_titles: %v", err)
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
	}
	d, err := s.store.DeviceStatusByUUID(uuid, s.now().Unix())
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	id, err := s.store.DeleteDevice(uuid)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("delete device: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	// Drop any pending rollups for the now-gone device, or the rollup ticker
	// would keep trying to write daily_stats for a missing device_id and fail
	// the foreign key forever.
	s.forgetDirtyDevice(id)
	log.Printf("deleted device %s (id %d) and its data", uuid, id)
	w.WriteHeader(http.StatusNoContent)
}

// forgetDirtyDevice removes every dirty (device, day) entry for one device.
func (s *Server) forgetDirtyDevice(deviceID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.dirty {
		if k.deviceID == deviceID {
			delete(s.dirty, k)
		}
	}
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if s.agentDir == "" {
		http.Error(w, "no release", http.StatusNotFound)
		return
	}
	// The signed manifest's sha256 is the integrity guarantee; a single current
	// agent.exe is served (the ?v= param is advisory).
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, filepath.Join(s.agentDir, "agent.exe"))
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	until := s.now()
	since := until.Add(-parseRange(r.URL.Query().Get("range")))
	rows, err := s.store.Summary(since.Unix(), until.Unix())
	if err != nil {
		log.Printf("summary: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []DeviceSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"since":   since.Unix(),
		"until":   until.Unix(),
		"devices": rows,
	})
}

// handleTrend returns household daily totals over the range, zero-filled so the
// chart axis is continuous.
func (s *Server) handleTrend(w http.ResponseWriter, r *http.Request) {
	until := s.now()
	since := until.Add(-parseRange(r.URL.Query().Get("range")))
	from := DayUTC(since.Unix())
	to := DayUTC(until.Unix())

	pts, err := s.store.Trend(from, to)
	if err != nil {
		log.Printf("trend: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	byDay := make(map[string]TrendPoint, len(pts))
	for _, p := range pts {
		byDay[p.Day] = p
	}
	start, _ := time.Parse("2006-01-02", from)
	end, _ := time.Parse("2006-01-02", to)
	dense := []TrendPoint{}
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		if p, ok := byDay[day]; ok {
			dense = append(dense, p)
		} else {
			dense = append(dense, TrendPoint{Day: day})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"trend": dense})
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	day := strings.TrimSpace(r.URL.Query().Get("day"))
	if day == "" {
		day = DayUTC(s.now().Unix())
	}
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		http.Error(w, "bad day (want YYYY-MM-DD)", http.StatusBadRequest)
		return
	}
	hours, err := s.store.DeviceTimeline(r.PathValue("uuid"), t.UTC().Unix())
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("timeline: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"day": day, "hours": hours})
}

func (s *Server) handleTopApps(w http.ResponseWriter, r *http.Request) {
	until := s.now()
	since := until.Add(-parseRange(r.URL.Query().Get("range")))
	limit := 10
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		if n > 50 {
			n = 50
		}
		limit = n
	}
	apps, err := s.store.DeviceTopApps(r.PathValue("uuid"), since.Unix(), until.Unix(), limit)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("top-apps: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if apps == nil {
		apps = []AppStat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

// handleSignals returns the per-day monitor-on / input-active / macro breakdown
// over the range (zero-filled), the data behind the signal-comparison view.
func (s *Server) handleSignals(w http.ResponseWriter, r *http.Request) {
	until := s.now()
	since := until.Add(-parseRange(r.URL.Query().Get("range")))
	fromDay := DayUTC(since.Unix())
	toDay := DayUTC(until.Unix())

	sig, err := s.store.DeviceSignals(r.PathValue("uuid"), dayStartUnix(fromDay), until.Unix())
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("signals: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	byDay := make(map[string]SignalDay, len(sig))
	for _, p := range sig {
		byDay[p.Day] = p
	}
	dense := []SignalDay{}
	start, _ := time.Parse("2006-01-02", fromDay)
	end, _ := time.Parse("2006-01-02", toDay)
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		if p, ok := byDay[day]; ok {
			dense = append(dense, p)
		} else {
			dense = append(dense, SignalDay{Day: day})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"signals": dense})
}

// handleHeatmap returns monitor-on minutes per (day, hour) for the last N days.
func (s *Server) handleHeatmap(w http.ResponseWriter, r *http.Request) {
	days := 14
	if n, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && n > 0 {
		if n > 35 {
			n = 35
		}
		days = n
	}
	until := s.now()
	startDay := DayUTC(until.Add(-time.Duration(days-1) * 24 * time.Hour).Unix())
	hm, err := s.store.DeviceHeatmap(r.PathValue("uuid"), dayStartUnix(startDay), until.Unix())
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("heatmap: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"heatmap": hm})
}

// dayStartUnix returns the UTC-midnight unix seconds for a YYYY-MM-DD string.
func dayStartUnix(day string) int64 {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return 0
	}
	return t.Unix()
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleSPA serves the embedded single-page app: real files (JS/CSS/assets) are
// served directly; any other path falls back to index.html so client-side routes
// like /devices/{uuid} resolve on a fresh load or refresh.
func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if s.spa == nil {
		http.Error(w, "dashboard not built (run scripts/build-ui.ps1)", http.StatusServiceUnavailable)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if _, err := fs.Stat(s.distFS, name); err != nil {
		r = r.Clone(r.Context()) // unknown path → SPA route → serve index.html
		r.URL.Path = "/"
	}
	s.spa.ServeHTTP(w, r)
}

// parseRange turns "7d"/"24h"/"30d" into a duration, defaulting to 7 days.
func parseRange(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 7 * 24 * time.Hour
	}
	unit := s[len(s)-1]
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 7 * 24 * time.Hour
	}
	switch unit {
	case 'h':
		return time.Duration(n) * time.Hour
	case 'd':
		return time.Duration(n) * 24 * time.Hour
	default:
		return 7 * 24 * time.Hour
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// bearerToken extracts a "Authorization: Bearer <token>" value, or "".
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// trueClientIP prefers Cloudflare's CF-Connecting-IP (set by the trusted edge)
// over the spoofable X-Forwarded-For, falling back to the socket peer (PLAN.md
// §6.3). Used only for enrollment audit + rate-limiting, never for auth.
func trueClientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// rateLimiter is a fixed-window per-key counter — small and dependency-free,
// sized for the low-volume enrollment endpoint (not the ingest hot path).
type rateLimiter struct {
	mu     sync.Mutex
	hits   map[string][]int64
	max    int
	window int64 // seconds
}

func newRateLimiter(max int, windowSecs int64) *rateLimiter {
	return &rateLimiter{hits: map[string][]int64{}, max: max, window: windowSecs}
}

// allow records an attempt for key at `now` and reports whether it is within the
// per-window cap. Entries older than the window are pruned on access, so the map
// stays bounded for the small set of enrolling IPs.
func (rl *rateLimiter) allow(key string, now int64) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := now - rl.window
	kept := rl.hits[key][:0]
	for _, t := range rl.hits[key] {
		if t > cutoff {
			kept = append(kept, t)
		}
	}
	if len(kept) >= rl.max {
		rl.hits[key] = kept
		return false
	}
	rl.hits[key] = append(kept, now)
	return true
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
