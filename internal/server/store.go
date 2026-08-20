package server

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

// Enrollment / device-auth errors (mapped to 401 by the handlers).
var (
	// ErrEnrollInvalid means the presented secret is unknown, expired, or already used.
	ErrEnrollInvalid = errors.New("enrollment token invalid, expired, or already used")
	// ErrNoDevice means no active (non-revoked) device holds the presented API token.
	ErrNoDevice = errors.New("no active device for token")
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
    agent_build         INTEGER,            -- monotonic build reported at ingest (behind-latest flag)
    monitor_detect_mode TEXT NOT NULL DEFAULT 'connection',
    api_token_hash      TEXT,               -- SHA-256 of the durable per-device token (NULL until enrolled)
    enroll_token_hash   TEXT,               -- SHA-256 of the one-time enrollment secret (NULL once consumed)
    enroll_expires      INTEGER,            -- unix seconds; the enrollment secret is void after this
    enrolled_at         INTEGER,
    enroll_ip           TEXT,               -- true client IP at enrollment (audit)
    revoked             INTEGER NOT NULL DEFAULT 0,
    log_titles          INTEGER NOT NULL DEFAULT 1, -- 0 = drop window titles on ingest (privacy, §9)
    clock_skew_seconds  INTEGER,            -- last agent-minus-server clock offset (NULL until the agent reports client_now)
    created_at          INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS samples (
    device_id       INTEGER NOT NULL REFERENCES devices(id),
    ts              INTEGER NOT NULL,
    monitor_on      INTEGER NOT NULL,
    monitors_active INTEGER,
    display_power   INTEGER,
    ddc_power       INTEGER,
    is_idle         INTEGER NOT NULL,
    idle_ms         INTEGER,
    exe_name        TEXT,
    window_title    TEXT,
    PRIMARY KEY (device_id, ts)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_samples_ts ON samples(ts);

CREATE TABLE IF NOT EXISTS monitor_events (
    device_id  INTEGER NOT NULL REFERENCES devices(id),
    ts         INTEGER NOT NULL,
    monitor_on INTEGER NOT NULL,
    PRIMARY KEY (device_id, ts)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS daily_stats (
    device_id       INTEGER NOT NULL REFERENCES devices(id),
    day             TEXT NOT NULL,
    monitor_minutes INTEGER NOT NULL DEFAULT 0,
    active_minutes  INTEGER NOT NULL DEFAULT 0,
    session_minutes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (device_id, day)
);

CREATE TABLE IF NOT EXISTS daily_app_stats (
    device_id       INTEGER NOT NULL REFERENCES devices(id),
    day             TEXT NOT NULL,
    exe_name        TEXT NOT NULL,
    monitor_minutes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (device_id, day, exe_name)
);

-- Append-only audit of notable per-device events, primarily agent self-updates
-- (and downgrades, which should never happen — a tamper signal). §8.
CREATE TABLE IF NOT EXISTS device_events (
    id        INTEGER PRIMARY KEY,
    device_id INTEGER NOT NULL REFERENCES devices(id),
    ts        INTEGER NOT NULL,
    kind      TEXT NOT NULL,              -- 'updated' | 'downgrade'
    detail    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_device_events ON device_events(device_id, ts);
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
	// Additive migrations for databases created before a column existed; on an
	// up-to-date schema the duplicate-column error is expected and ignored.
	for _, stmt := range []string{
		`ALTER TABLE samples ADD COLUMN ddc_power INTEGER`,
		`ALTER TABLE devices ADD COLUMN api_token_hash TEXT`,
		`ALTER TABLE devices ADD COLUMN enroll_token_hash TEXT`,
		`ALTER TABLE devices ADD COLUMN enroll_expires INTEGER`,
		`ALTER TABLE devices ADD COLUMN enrolled_at INTEGER`,
		`ALTER TABLE devices ADD COLUMN enroll_ip TEXT`,
		`ALTER TABLE devices ADD COLUMN revoked INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE devices ADD COLUMN log_titles INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE devices ADD COLUMN agent_build INTEGER`,
		`ALTER TABLE devices ADD COLUMN clock_skew_seconds INTEGER`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, err
		}
	}
	// Token lookups are per-ingest hot-path; index them (after the ALTERs so the
	// columns exist on migrated databases).
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_devices_api_token ON devices(api_token_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_devices_enroll_token ON devices(enroll_token_hash)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

// randomToken returns a 256-bit secret as hex (enrollment secrets + API tokens).
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// randomUUID returns a 128-bit opaque device identifier as hex.
func randomUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hashToken is the at-rest form of every secret — only hashes are stored, so a
// database leak never yields a usable enrollment secret or API token.
func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// PrepareEnrollment creates a pending device slot and returns its UUID plus a
// one-time enrollment secret (returned in plaintext exactly once; only its hash
// is stored). The secret is valid until `expires`.
func (s *Store) PrepareEnrollment(name string, now, expires int64) (uuid, secret string, err error) {
	uuid, err = randomUUID()
	if err != nil {
		return "", "", err
	}
	secret, err = randomToken()
	if err != nil {
		return "", "", err
	}
	_, err = s.db.Exec(`
		INSERT INTO devices (device_uuid, name, enroll_token_hash, enroll_expires, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		uuid, name, hashToken(secret), expires, now)
	if err != nil {
		return "", "", err
	}
	return uuid, secret, nil
}

// ConsumeEnrollment validates a one-time secret and, atomically, issues a
// durable API token for that device. Single-use is enforced by requiring
// api_token_hash IS NULL and setting it in the same transaction (the store's
// single writer serializes concurrent attempts). Returns ErrEnrollInvalid if no
// unused, unexpired, non-revoked slot matches.
func (s *Store) ConsumeEnrollment(secret, hostname, ip string, now int64) (uuid, apiToken string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRow(`
		SELECT id, device_uuid FROM devices
		WHERE enroll_token_hash = ? AND api_token_hash IS NULL AND revoked = 0 AND enroll_expires >= ?`,
		hashToken(secret), now).Scan(&id, &uuid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrEnrollInvalid
	}
	if err != nil {
		return "", "", err
	}

	apiToken, err = randomToken()
	if err != nil {
		return "", "", err
	}
	if _, err = tx.Exec(`
		UPDATE devices
		SET api_token_hash = ?, enrolled_at = ?, enroll_ip = ?, hostname = ?, last_seen = ?,
		    enroll_token_hash = NULL, enroll_expires = NULL
		WHERE id = ?`,
		hashToken(apiToken), now, ip, hostname, now, id); err != nil {
		return "", "", err
	}
	if err = tx.Commit(); err != nil {
		return "", "", err
	}
	return uuid, apiToken, nil
}

// DeviceByToken returns the device id for a presented API token, rejecting
// unknown and revoked tokens with ErrNoDevice (both → 401, indistinguishable to
// a caller holding a bad token).
func (s *Store) DeviceByToken(apiToken string) (int64, error) {
	var id int64
	var revoked int
	err := s.db.QueryRow(`SELECT id, revoked FROM devices WHERE api_token_hash = ?`,
		hashToken(apiToken)).Scan(&id, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoDevice
	}
	if err != nil {
		return 0, err
	}
	if revoked != 0 {
		return 0, ErrNoDevice
	}
	return id, nil
}

// TouchDevice updates liveness fields for an authenticated device. An empty
// hostname leaves the stored value untouched (COALESCE), so a sparse ingest
// never blanks it. skew carries the agent-minus-server clock offset when the
// agent reported its wall-clock; an invalid skew keeps the last-known value
// (older agents don't send client_now, §8 clock-skew flag).
func (s *Store) TouchDevice(id int64, hostname, version string, build int64, skew sql.NullInt64, now int64) error {
	// Capture the prior build so a change in it can be recorded as a self-update
	// event (tamper-evidence, §8). Cheap: one indexed lookup on a tiny table.
	var oldBuild sql.NullInt64
	var oldVer sql.NullString
	_ = s.db.QueryRow(`SELECT agent_build, agent_version FROM devices WHERE id = ?`, id).Scan(&oldBuild, &oldVer)

	if _, err := s.db.Exec(`
		UPDATE devices SET last_seen = ?, agent_version = ?, agent_build = ?,
		    hostname = COALESCE(NULLIF(?, ''), hostname),
		    clock_skew_seconds = COALESCE(?, clock_skew_seconds)
		WHERE id = ?`,
		now, version, build, hostname, skew, id); err != nil {
		return err
	}

	// Log a build transition only when there was a prior build to compare against
	// (skip the first-ever check-in). A backwards move is a downgrade — an agent
	// should never self-update to a lower build, so it's flagged distinctly.
	if build != 0 && oldBuild.Valid && oldBuild.Int64 != 0 && oldBuild.Int64 != build {
		kind := "updated"
		if build < oldBuild.Int64 {
			kind = "downgrade"
		}
		detail := fmt.Sprintf("%s (build %d) → %s (build %d)",
			orDash(oldVer.String), oldBuild.Int64, orDash(version), build)
		if _, err := s.db.Exec(
			`INSERT INTO device_events (device_id, ts, kind, detail) VALUES (?, ?, ?, ?)`,
			id, now, kind, detail); err != nil {
			log.Printf("record device event: %v", err) // best-effort audit; never fail ingest
		}
	}
	return nil
}

// orDash renders an empty version string as "?" for readable audit detail.
func orDash(v string) string {
	if v == "" {
		return "?"
	}
	return v
}

// SetRevoked flips a device's revoked flag by UUID. A revoked device's next
// ingest fails auth (§Phase 3 verify: revoke → uploads 401).
func (s *Store) SetRevoked(uuid string, revoked bool) error {
	_, err := s.db.Exec(`UPDATE devices SET revoked = ? WHERE device_uuid = ?`, boolToInt(revoked), uuid)
	return err
}

// DeviceLogTitles reports whether window titles should be stored for a device.
// A missing row defaults to true (log titles) — the safe default is to keep the
// data the operator enrolled for; opting out is the explicit action.
func (s *Store) DeviceLogTitles(id int64) (bool, error) {
	var v int
	err := s.db.QueryRow(`SELECT log_titles FROM devices WHERE id = ?`, id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return v != 0, err
}

// SetLogTitles sets a device's title-logging preference by UUID.
func (s *Store) SetLogTitles(uuid string, on bool) error {
	_, err := s.db.Exec(`UPDATE devices SET log_titles = ? WHERE device_uuid = ?`, boolToInt(on), uuid)
	return err
}

// RenameDevice sets a device's display name by UUID.
func (s *Store) RenameDevice(uuid, name string) error {
	_, err := s.db.Exec(`UPDATE devices SET name = ? WHERE device_uuid = ?`, name, uuid)
	return err
}

// DeleteDevice removes a device and ALL of its data (raw samples, monitor
// events, and daily rollups), returning the deleted row id. Unlike revoke —
// which keeps the row and its history but blocks the token — this erases the
// device entirely, for clearing out test/duplicate/decommissioned machines.
// Returns sql.ErrNoRows if the UUID is unknown.
func (s *Store) DeleteDevice(uuid string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var id int64
	if err := tx.QueryRow(`SELECT id FROM devices WHERE device_uuid = ?`, uuid).Scan(&id); err != nil {
		return 0, err // sql.ErrNoRows when unknown
	}
	// Children first (foreign_keys=ON would otherwise reject the devices delete).
	for _, stmt := range []string{
		`DELETE FROM samples WHERE device_id = ?`,
		`DELETE FROM monitor_events WHERE device_id = ?`,
		`DELETE FROM daily_stats WHERE device_id = ?`,
		`DELETE FROM daily_app_stats WHERE device_id = ?`,
		`DELETE FROM device_events WHERE device_id = ?`,
		`DELETE FROM devices WHERE id = ?`,
	} {
		if _, err := tx.Exec(stmt, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

const deviceStatusCols = `device_uuid, name, COALESCE(hostname, ''), COALESCE(last_seen, 0),
	COALESCE(agent_version, ''), COALESCE(agent_build, 0), api_token_hash IS NOT NULL, revoked,
	enroll_expires, COALESCE(enrolled_at, 0), log_titles, clock_skew_seconds`

func scanDeviceStatus(sc interface{ Scan(...any) error }, now int64) (shared.DeviceStatus, error) {
	var d shared.DeviceStatus
	var apiSet, revoked, logTitles int
	var enrollExpires, clockSkew sql.NullInt64
	if err := sc.Scan(&d.DeviceUUID, &d.Name, &d.Hostname, &d.LastSeen,
		&d.AgentVersion, &d.AgentBuild, &apiSet, &revoked, &enrollExpires, &d.EnrolledAt, &logTitles, &clockSkew); err != nil {
		return d, err
	}
	d.LogTitles = logTitles != 0
	d.ClockSkew, d.ClockSkewKnown = clockSkew.Int64, clockSkew.Valid
	switch {
	case revoked != 0:
		d.Status = "revoked"
	case apiSet != 0:
		d.Status = "active"
	case enrollExpires.Valid && enrollExpires.Int64 < now:
		d.Status = "expired"
	default:
		d.Status = "pending"
	}
	return d, nil
}

// DeviceStatuses lists every device with a derived status, newest first.
func (s *Store) DeviceStatuses(now int64) ([]shared.DeviceStatus, error) {
	rows, err := s.db.Query(`SELECT ` + deviceStatusCols + ` FROM devices ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []shared.DeviceStatus
	for rows.Next() {
		d, err := scanDeviceStatus(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeviceStatusByUUID returns one device's status, or sql.ErrNoRows if unknown.
func (s *Store) DeviceStatusByUUID(uuid string, now int64) (shared.DeviceStatus, error) {
	row := s.db.QueryRow(`SELECT `+deviceStatusCols+` FROM devices WHERE device_uuid = ?`, uuid)
	return scanDeviceStatus(row, now)
}

// DeviceEvents returns a device's audit events (newest first), for the "update
// history" panel. An unknown UUID yields an empty slice, not an error.
func (s *Store) DeviceEvents(uuid string, limit int) ([]shared.DeviceEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT e.ts, e.kind, e.detail
		FROM device_events e JOIN devices d ON d.id = e.device_id
		WHERE d.device_uuid = ?
		ORDER BY e.ts DESC, e.id DESC
		LIMIT ?`, uuid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []shared.DeviceEvent
	for rows.Next() {
		var e shared.DeviceEvent
		if err := rows.Scan(&e.TS, &e.Kind, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
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
// Returns rows inserted and the set of UTC days touched (for dirty-days rollup).
func (s *Store) InsertSamples(deviceID int64, samples []shared.Sample, serverNow int64) (int, map[string]bool, error) {
	days := map[string]bool{}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, days, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO samples
			(device_id, ts, monitor_on, monitors_active, display_power, ddc_power, is_idle, idle_ms, exe_name, window_title)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, days, err
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
			nullableDDC(smp.DDCPower),
			boolToInt(smp.IsIdle), smp.IdleMS, smp.Exe, truncate(smp.Title, maxTitleLen),
		)
		if err != nil {
			return accepted, days, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			accepted++
			days[DayUTC(smp.ClientTS)] = true
		}
	}
	if err := tx.Commit(); err != nil {
		return accepted, days, err
	}
	return accepted, days, nil
}

// InsertMonitorEvents inserts on/off transition events idempotently, clamping
// timestamps like samples. Returns rows inserted and the set of UTC days touched
// (for the dirty-days rollup, PLAN.md §6.3).
func (s *Store) InsertMonitorEvents(deviceID int64, events []shared.MonitorEvent, serverNow int64) (int, map[string]bool, error) {
	days := map[string]bool{}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, days, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO monitor_events (device_id, ts, monitor_on) VALUES (?, ?, ?)`)
	if err != nil {
		return 0, days, err
	}
	defer stmt.Close()

	accepted := 0
	for _, e := range events {
		if e.ClientTS < serverNow-maxBackdateSecs || e.ClientTS > serverNow+maxFutureSecs {
			continue
		}
		on := 0
		if e.MonitorOn != 0 {
			on = 1
		}
		res, err := stmt.Exec(deviceID, e.ClientTS, on)
		if err != nil {
			return accepted, days, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			accepted++
			days[DayUTC(e.ClientTS)] = true
		}
	}
	if err := tx.Commit(); err != nil {
		return accepted, days, err
	}
	return accepted, days, nil
}

// DayUTC formats a unix timestamp as YYYY-MM-DD in UTC. (Phase 2 uses UTC; a
// configurable household timezone is a later refinement — PLAN.md §6.2.)
func DayUTC(ts int64) string {
	return time.Unix(ts, 0).UTC().Format("2006-01-02")
}

// MonitorOnSeconds returns the exact number of seconds the monitor was on in
// [from, to), integrated from the transition events (PLAN.md §4.5). The state at
// `from` is taken from the most recent event at or before it; if there is none,
// the monitor is assumed off until the first recorded transition.
func (s *Store) MonitorOnSeconds(deviceID, from, to int64) (int64, error) {
	if to <= from {
		return 0, nil
	}
	// Initial state = last event at/before `from`.
	state := 0
	var prevTS int64
	err := s.db.QueryRow(
		`SELECT monitor_on FROM monitor_events WHERE device_id=? AND ts<=? ORDER BY ts DESC LIMIT 1`,
		deviceID, from).Scan(&state)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	prevTS = from

	rows, err := s.db.Query(
		`SELECT ts, monitor_on FROM monitor_events WHERE device_id=? AND ts>? AND ts<? ORDER BY ts`,
		deviceID, from, to)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var onSecs int64
	for rows.Next() {
		var ts int64
		var on int
		if err := rows.Scan(&ts, &on); err != nil {
			return 0, err
		}
		if state == 1 {
			onSecs += ts - prevTS
		}
		state = on
		prevTS = ts
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if state == 1 {
		onSecs += to - prevTS
	}
	return onSecs, nil
}

// RollupDay recomputes the permanent daily aggregates for one device/day from raw
// samples (and exact monitor-on from events when any exist for that day). It is
// idempotent, so re-running it for a late-arriving backlog is safe. The window is
// capped at `now` so an unterminated "on" interval on the current day isn't
// integrated into the future (which would massively overcount today's minutes).
func (s *Store) RollupDay(deviceID int64, day string, now int64) error {
	// Skip a device that no longer exists (e.g. deleted mid-cycle). The
	// daily_stats upsert below would otherwise FK-fail with foreign_keys=ON, and
	// RunRollups re-marks the day on any error — an endless per-tick error loop.
	// This guard closes that regardless of any delete-vs-ingest ordering race.
	var exists int
	if err := s.db.QueryRow(`SELECT 1 FROM devices WHERE id=?`, deviceID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}

	dayStart, err := time.Parse("2006-01-02", day)
	if err != nil {
		return err
	}
	from := dayStart.UTC().Unix()
	to := from + 86400
	if now < to {
		to = now
	}
	if to <= from {
		return nil // day is entirely in the future; nothing to roll up yet
	}

	var monitorMin, activeMin, sessionMin int
	err = s.db.QueryRow(`
		SELECT COALESCE(SUM(CASE WHEN monitor_on=1 THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN monitor_on=1 AND is_idle=0 THEN 1 ELSE 0 END),0),
		       COUNT(*)
		FROM samples WHERE device_id=? AND ts>=? AND ts<?`, deviceID, from, to).
		Scan(&monitorMin, &activeMin, &sessionMin)
	if err != nil {
		return err
	}

	// Prefer exact monitor-on minutes from transition events when present.
	var eventCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM monitor_events WHERE device_id=? AND ts>=? AND ts<?`,
		deviceID, from, to).Scan(&eventCount); err != nil {
		return err
	}
	if eventCount > 0 {
		secs, err := s.MonitorOnSeconds(deviceID, from, to)
		if err != nil {
			return err
		}
		monitorMin = int((secs + 30) / 60) // round to nearest minute
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		INSERT INTO daily_stats (device_id, day, monitor_minutes, active_minutes, session_minutes)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(device_id, day) DO UPDATE SET
			monitor_minutes=excluded.monitor_minutes,
			active_minutes=excluded.active_minutes,
			session_minutes=excluded.session_minutes`,
		deviceID, day, monitorMin, activeMin, sessionMin); err != nil {
		return err
	}

	// Per-app rollup: replace this day's rows.
	if _, err := tx.Exec(`DELETE FROM daily_app_stats WHERE device_id=? AND day=?`, deviceID, day); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO daily_app_stats (device_id, day, exe_name, monitor_minutes)
		SELECT device_id, ?, COALESCE(exe_name,''), COUNT(*)
		FROM samples WHERE device_id=? AND ts>=? AND ts<? AND monitor_on=1
		GROUP BY exe_name`, day, deviceID, from, to); err != nil {
		return err
	}
	return tx.Commit()
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

// deviceIDByUUID resolves a device UUID to its row id (sql.ErrNoRows if unknown).
func (s *Store) deviceIDByUUID(uuid string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM devices WHERE device_uuid = ?`, uuid).Scan(&id)
	return id, err
}

// TrendPoint is one day of household-wide totals (summed across devices).
type TrendPoint struct {
	Day            string `json:"day"`
	MonitorMinutes int    `json:"monitor_minutes"`
	ActiveMinutes  int    `json:"active_minutes"`
}

// Trend returns household daily totals for days in [fromDay, toDay] (inclusive),
// from the permanent rollups. Sparse — only days with data are returned; the
// handler zero-fills the rest so the chart has a continuous axis.
func (s *Store) Trend(fromDay, toDay string) ([]TrendPoint, error) {
	rows, err := s.db.Query(`
		SELECT day, COALESCE(SUM(monitor_minutes),0), COALESCE(SUM(active_minutes),0)
		FROM daily_stats WHERE day >= ? AND day <= ?
		GROUP BY day ORDER BY day`, fromDay, toDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrendPoint
	for rows.Next() {
		var p TrendPoint
		if err := rows.Scan(&p.Day, &p.MonitorMinutes, &p.ActiveMinutes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// HourBucket is one hour of a device's day (monitor-on vs input-active minutes).
type HourBucket struct {
	Hour           int `json:"hour"`
	MonitorMinutes int `json:"monitor_minutes"`
	ActiveMinutes  int `json:"active_minutes"`
}

// DeviceTimeline returns 24 zero-filled hourly buckets for the day starting at
// dayStart (an epoch — the caller chooses the zone; the handler passes local
// midnight), computed from raw samples (works without a rollup having run). Hours
// are relative to dayStart, so a local midnight yields local hour buckets.
func (s *Store) DeviceTimeline(uuid string, dayStart int64) ([]HourBucket, error) {
	buckets := make([]HourBucket, 24)
	for h := range buckets {
		buckets[h].Hour = h
	}
	id, err := s.deviceIDByUUID(uuid)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`
		SELECT (ts - ?) / 3600 AS hour,
		       SUM(CASE WHEN monitor_on=1 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN monitor_on=1 AND is_idle=0 THEN 1 ELSE 0 END)
		FROM samples WHERE device_id = ? AND ts >= ? AND ts < ?
		GROUP BY hour`, dayStart, id, dayStart, dayStart+86400)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h, mon, act int
		if err := rows.Scan(&h, &mon, &act); err != nil {
			return nil, err
		}
		if h >= 0 && h < 24 {
			buckets[h].MonitorMinutes = mon
			buckets[h].ActiveMinutes = act
		}
	}
	return buckets, rows.Err()
}

// AppStat is one foreground app's monitor-on minutes over a range.
type AppStat struct {
	Exe            string `json:"exe"`
	MonitorMinutes int    `json:"monitor_minutes"`
}

// DeviceTopApps returns a device's top foreground apps by monitor-on minutes for
// ts in [fromUnix, toUnix).
//
// This reads RAW samples, like the timeline and summary queries — not the
// daily_app_stats rollup. Reading the rollup made this the only panel that
// depended on the (hourly) rollup ticker having run, so a freshly enrolled
// device showed an empty "Top apps" for up to an hour while every other panel
// had data. Raw retention (~90d; purge deferred, PLAN.md §6.2) comfortably
// covers every range the UI offers, and the rollup stays the permanent
// post-purge record.
func (s *Store) DeviceTopApps(uuid string, fromUnix, toUnix int64, limit int) ([]AppStat, error) {
	id, err := s.deviceIDByUUID(uuid)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`
		SELECT COALESCE(NULLIF(exe_name,''),'(unknown)') AS exe, COUNT(*) AS mins
		FROM samples WHERE device_id = ? AND monitor_on = 1 AND ts >= ? AND ts < ?
		GROUP BY exe ORDER BY mins DESC, exe LIMIT ?`, id, fromUnix, toUnix, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppStat
	for rows.Next() {
		var a AppStat
		if err := rows.Scan(&a.Exe, &a.MonitorMinutes); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// TitleStat is one distinct foreground window title with the screen-on minutes it
// was in front and when it was last seen.
type TitleStat struct {
	Title    string `json:"title"`
	Exe      string `json:"exe"`
	Minutes  int    `json:"minutes"`
	LastSeen int64  `json:"last_seen"`
}

// DeviceTitles returns distinct foreground window titles for a device over
// [fromUnix,toUnix), each with the number of screen-on minutes it was in front and
// when it was last seen, most time first. Only screen-on samples count (matching
// the "screentime" framing). Optional exe and case-insensitive substring filters
// narrow the result. Empty titles — never captured, or dropped by the per-device
// title opt-out (§9) — are excluded, so an opted-out device simply yields nothing.
func (s *Store) DeviceTitles(uuid string, fromUnix, toUnix int64, exe, q string, limit int) ([]TitleStat, error) {
	id, err := s.deviceIDByUUID(uuid)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.Query(`
		SELECT window_title,
		       COALESCE(NULLIF(exe_name,''),'(unknown)') AS exe,
		       COUNT(*) AS mins, MAX(ts) AS last
		FROM samples
		WHERE device_id = ? AND monitor_on = 1 AND ts >= ? AND ts < ?
		  AND window_title IS NOT NULL AND window_title <> ''
		  AND (? = '' OR COALESCE(NULLIF(exe_name,''),'(unknown)') = ?)
		  AND (? = '' OR window_title LIKE '%' || ? || '%')
		GROUP BY window_title, exe
		ORDER BY mins DESC, last DESC
		LIMIT ?`, id, fromUnix, toUnix, exe, exe, q, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TitleStat
	for rows.Next() {
		var t TitleStat
		if err := rows.Scan(&t.Title, &t.Exe, &t.Minutes, &t.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeviceTitleApps lists the distinct apps (exe) that have any logged window title
// in the range, for the titles page's app filter. Range-scoped but not narrowed by
// the exe/search filters, so the dropdown always offers every app.
func (s *Store) DeviceTitleApps(uuid string, fromUnix, toUnix int64) ([]string, error) {
	id, err := s.deviceIDByUUID(uuid)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`
		SELECT DISTINCT COALESCE(NULLIF(exe_name,''),'(unknown)') AS exe
		FROM samples
		WHERE device_id = ? AND monitor_on = 1 AND ts >= ? AND ts < ?
		  AND window_title IS NOT NULL AND window_title <> ''
		ORDER BY exe`, id, fromUnix, toUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeviceDay identifies one device's UTC day, the unit the rollup works on.
type DeviceDay struct {
	DeviceID int64
	Day      string
}

// RecentSampleDays lists the (device, UTC day) pairs that have samples at or
// after sinceUnix. The dirty set that drives rollups lives in memory, so a
// restart would otherwise silently drop any day that was ingested but not yet
// rolled up (PLAN.md §6.3 warns these minutes "silently vanish"). Re-marking
// recent days at startup makes that self-healing; the rollup is idempotent, so
// redoing a day costs nothing.
func (s *Store) RecentSampleDays(sinceUnix int64) ([]DeviceDay, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT device_id, strftime('%Y-%m-%d', ts, 'unixepoch')
		FROM samples WHERE ts >= ?`, sinceUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceDay
	for rows.Next() {
		var d DeviceDay
		if err := rows.Scan(&d.DeviceID, &d.Day); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SignalDay is one day's breakdown of the three signals that tell the §0.1
// story. macro_minutes — input while the monitor is OFF — is the autoclicker
// fingerprint the whole design hinges on; it should normally be ~0.
type SignalDay struct {
	Day            string `json:"day"`
	MonitorMinutes int    `json:"monitor_minutes"` // monitor on
	ActiveMinutes  int    `json:"active_minutes"`  // monitor on AND input active (engaged)
	MacroMinutes   int    `json:"macro_minutes"`   // monitor OFF AND input active (macro fingerprint)
	SessionMinutes int    `json:"session_minutes"` // any sample present
}

// DeviceSignals returns the per-day signal breakdown from raw samples for ts in
// [fromUnix, toUnix). Sparse; the handler zero-fills the day axis.
func (s *Store) DeviceSignals(uuid string, fromUnix, toUnix int64) ([]SignalDay, error) {
	id, err := s.deviceIDByUUID(uuid)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`
		SELECT strftime('%Y-%m-%d', ts, 'unixepoch') AS day,
		       SUM(CASE WHEN monitor_on=1 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN monitor_on=1 AND is_idle=0 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN monitor_on=0 AND is_idle=0 THEN 1 ELSE 0 END),
		       COUNT(*)
		FROM samples WHERE device_id = ? AND ts >= ? AND ts < ?
		GROUP BY day ORDER BY day`, id, fromUnix, toUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SignalDay
	for rows.Next() {
		var d SignalDay
		if err := rows.Scan(&d.Day, &d.MonitorMinutes, &d.ActiveMinutes, &d.MacroMinutes, &d.SessionMinutes); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// HeatDay is one row of the activity heatmap: monitor-on minutes per UTC hour.
type HeatDay struct {
	Day   string  `json:"day"`
	Hours [24]int `json:"hours"`
}

// DeviceHeatmap returns monitor-on minutes bucketed by (UTC day, hour) for ts in
// [fromUnix, toUnix), one dense HeatDay per day in range (zero-filled).
func (s *Store) DeviceHeatmap(uuid string, fromUnix, toUnix int64) ([]HeatDay, error) {
	id, err := s.deviceIDByUUID(uuid)
	if err != nil {
		return nil, err
	}
	// Dense day axis first (so days with no activity still appear as a row).
	byDay := map[string]*HeatDay{}
	var days []HeatDay
	for t := fromUnix - (fromUnix % 86400); t < toUnix; t += 86400 {
		day := time.Unix(t, 0).UTC().Format("2006-01-02")
		days = append(days, HeatDay{Day: day})
	}
	for i := range days {
		byDay[days[i].Day] = &days[i]
	}

	rows, err := s.db.Query(`
		SELECT ts / 86400 AS day_epoch, (ts % 86400) / 3600 AS hour, COUNT(*)
		FROM samples WHERE device_id = ? AND monitor_on = 1 AND ts >= ? AND ts < ?
		GROUP BY day_epoch, hour`, id, fromUnix, toUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var dayEpoch, hour, n int64
		if err := rows.Scan(&dayEpoch, &hour, &n); err != nil {
			return nil, err
		}
		day := time.Unix(dayEpoch*86400, 0).UTC().Format("2006-01-02")
		if hd, ok := byDay[day]; ok && hour >= 0 && hour < 24 {
			hd.Hours[hour] = int(n)
		}
	}
	return days, rows.Err()
}

func nullableInt(v int) any {
	if v < 0 {
		return nil
	}
	return v
}

// nullableDDC stores the raw DDC probe value. Unlike the other monitor signals
// its meaningful values include negatives (-1 no handle, -2 query failed);
// only "not sampled" (-3, or 0 from agents predating the field) becomes NULL.
func nullableDDC(v int) any {
	if v == 0 || v == -3 {
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
