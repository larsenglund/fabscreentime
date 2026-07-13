package server

import (
	"database/sql"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

// clamp window for agent-supplied timestamps (PLAN.md §6.3). Rows outside this
// window are rejected so a wrong clock or a leaked token can't poison rollups.
const (
	maxBackdateSecs = 2 * 24 * 3600 // 2 days
	maxFutureSecs   = 300           // 5 minutes
	maxTitleLen     = 512
)

// schema is the Phase 0 subset of PLAN.md §6.2 (monitor_events / daily rollups /
// tokens arrive in later phases). Columns match the plan so later phases only add.
const schema = `
CREATE TABLE IF NOT EXISTS devices (
    id                  INTEGER PRIMARY KEY,
    device_uuid         TEXT NOT NULL UNIQUE,
    name                TEXT NOT NULL DEFAULT '',
    hostname            TEXT,
    last_seen           INTEGER,
    agent_version       TEXT,
    monitor_detect_mode TEXT NOT NULL DEFAULT 'connection',
    created_at          INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS samples (
    device_id       INTEGER NOT NULL REFERENCES devices(id),
    ts              INTEGER NOT NULL,
    monitor_on      INTEGER NOT NULL,
    monitors_active INTEGER,
    display_power   INTEGER,
    is_idle         INTEGER NOT NULL,
    idle_ms         INTEGER,
    exe_name        TEXT,
    window_title    TEXT,
    PRIMARY KEY (device_id, ts)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_samples_ts ON samples(ts);
`

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// OpenStore opens (creating if needed) the SQLite database at path.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, err
		}
	}
	// Single writer keeps a lean embedded DB free of lock contention at this scale.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// UpsertDevice inserts or updates a device by UUID and returns its row id.
func (s *Store) UpsertDevice(uuid, hostname, version string, now int64) (int64, error) {
	_, err := s.db.Exec(`
		INSERT INTO devices (device_uuid, hostname, agent_version, last_seen, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(device_uuid) DO UPDATE SET
			hostname      = excluded.hostname,
			agent_version = excluded.agent_version,
			last_seen     = excluded.last_seen`,
		uuid, hostname, version, now, now)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(`SELECT id FROM devices WHERE device_uuid = ?`, uuid).Scan(&id)
	return id, err
}

// InsertSamples inserts a batch idempotently (INSERT OR IGNORE on the (device,ts)
// PK dedups retried batches). Timestamps are clamped/rejected against serverNow.
// Returns the number of rows actually inserted.
func (s *Store) InsertSamples(deviceID int64, samples []shared.Sample, serverNow int64) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO samples
			(device_id, ts, monitor_on, monitors_active, display_power, is_idle, idle_ms, exe_name, window_title)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	accepted := 0
	for _, smp := range samples {
		if smp.ClientTS < serverNow-maxBackdateSecs || smp.ClientTS > serverNow+maxFutureSecs {
			continue // reject out-of-window timestamps rather than clamp-and-collide
		}
		res, err := stmt.Exec(
			deviceID, smp.ClientTS, smp.MonitorOn,
			nullableInt(smp.MonitorsActive), nullableInt(smp.DisplayPower),
			boolToInt(smp.IsIdle), smp.IdleMS, smp.Exe, truncate(smp.Title, maxTitleLen),
		)
		if err != nil {
			return accepted, err
		}
		n, _ := res.RowsAffected()
		accepted += int(n)
	}
	if err := tx.Commit(); err != nil {
		return accepted, err
	}
	return accepted, nil
}

// DeviceSummary is one row of the dashboard overview.
type DeviceSummary struct {
	DeviceUUID     string `json:"device_uuid"`
	Name           string `json:"name"`
	Hostname       string `json:"hostname"`
	LastSeen       int64  `json:"last_seen"`
	AgentVersion   string `json:"agent_version"`
	MonitorMinutes int    `json:"monitor_minutes"` // ≈ count of monitor_on samples (each ~1 min)
	ActiveMinutes  int    `json:"active_minutes"`  // monitor_on AND NOT idle
	SessionMinutes int    `json:"session_minutes"` // any sample present
	SampleCount    int    `json:"sample_count"`
}

// Summary returns per-device aggregates for samples in [since, until).
func (s *Store) Summary(since, until int64) ([]DeviceSummary, error) {
	rows, err := s.db.Query(`
		SELECT d.device_uuid, d.name, COALESCE(d.hostname,''),
		       COALESCE(d.last_seen,0), COALESCE(d.agent_version,''),
		       COALESCE(SUM(CASE WHEN s.monitor_on=1 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN s.monitor_on=1 AND s.is_idle=0 THEN 1 ELSE 0 END), 0),
		       COUNT(s.ts)
		FROM devices d
		LEFT JOIN samples s ON s.device_id = d.id AND s.ts >= ? AND s.ts < ?
		GROUP BY d.id
		ORDER BY d.last_seen DESC`, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DeviceSummary
	for rows.Next() {
		var d DeviceSummary
		if err := rows.Scan(&d.DeviceUUID, &d.Name, &d.Hostname, &d.LastSeen,
			&d.AgentVersion, &d.MonitorMinutes, &d.ActiveMinutes, &d.SampleCount); err != nil {
			return nil, err
		}
		d.SessionMinutes = d.SampleCount
		out = append(out, d)
	}
	return out, rows.Err()
}

func nullableInt(v int) any {
	if v < 0 {
		return nil
	}
	return v
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Trim on a rune boundary to avoid splitting a multibyte character.
	return strings.ToValidUTF8(s[:n], "")
}
