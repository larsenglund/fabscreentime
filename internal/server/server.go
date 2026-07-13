package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

const maxIngestBytes = 1 << 20 // 1 MiB body cap (PLAN.md §6.3)

// Server holds the HTTP handlers and their dependencies.
type Server struct {
	store *Store
	now   func() time.Time
}

// New returns a Server backed by store.
func New(store *Store) *Server {
	return &Server{store: store, now: time.Now}
}

// Handler builds the request router (Go 1.22+ method+path patterns, no framework).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/ingest", s.handleIngest)
	mux.HandleFunc("GET /api/dashboard/summary", s.handleSummary)
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
	accepted, err := s.store.InsertSamples(id, req.Samples, now)
	if err != nil {
		log.Printf("insert samples: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, shared.IngestResponse{
		Accepted:   accepted,
		ServerTime: now,
		Update:     shared.UpdateInfo{Available: false}, // signed updates arrive in Phase 1
	})
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
