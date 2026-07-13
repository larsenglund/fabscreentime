package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

const maxIngestBytes = 1 << 20 // 1 MiB body cap (PLAN.md §6.3)

type dirtyKey struct {
	deviceID int64
	day      string
}

// Server holds the HTTP handlers and their dependencies.
type Server struct {
	store    *Store
	now      func() time.Time
	agentDir string                 // holds manifest.json + agent.exe (may be empty)
	manifest *shared.SignedManifest // current signed release, loaded at startup

	mu    sync.Mutex
	dirty map[dirtyKey]bool // (device, day) pairs whose rollup is stale
}

// New returns a Server backed by store. agentDir, if non-empty, is scanned for a
// signed agent release (manifest.json + agent.exe) to serve for auto-update.
func New(store *Store, agentDir string) *Server {
	s := &Server{store: store, now: time.Now, agentDir: agentDir, dirty: map[dirtyKey]bool{}}
	s.loadManifest()
	return s
}

// loadManifest reads agentDir/manifest.json if present. The backend is not
// auto-updated, so this is done once at startup; publishing a new release means
// dropping new files and restarting (PLAN.md §8.1).
func (s *Server) loadManifest() {
	if s.agentDir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(s.agentDir, "manifest.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("agent manifest: %v", err)
		}
		return
	}
	var sm shared.SignedManifest
	if err := json.Unmarshal(data, &sm); err != nil {
		log.Printf("agent manifest parse: %v", err)
		return
	}
	s.manifest = &sm
	log.Printf("serving agent release %s (build %d)", sm.Manifest.Version, sm.Manifest.Build)
}

// Handler builds the request router (Go 1.22+ method+path patterns, no framework).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/ingest", s.handleIngest)
	mux.HandleFunc("GET /api/dashboard/summary", s.handleSummary)
	mux.HandleFunc("GET /agent/manifest", s.handleManifest)
	mux.HandleFunc("GET /agent/download", s.handleDownload)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /", s.handleIndex)
	return logRequests(mux)
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxIngestBytes)
	var req shared.IngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.DeviceUUID) == "" {
		http.Error(w, "device_uuid required", http.StatusBadRequest)
		return
	}
	now := s.now().Unix()
	id, err := s.store.UpsertDevice(req.DeviceUUID, req.Hostname, req.AgentVersion, now)
	if err != nil {
		log.Printf("upsert device: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
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
	if s.manifest == nil {
		return shared.UpdateInfo{Available: false}
	}
	return shared.UpdateInfo{
		Available: s.manifest.Manifest.Build > agentBuild,
		Manifest:  s.manifest,
	}
}

func (s *Server) handleManifest(w http.ResponseWriter, _ *http.Request) {
	if s.manifest == nil {
		http.Error(w, "no release", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.manifest)
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

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
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

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
