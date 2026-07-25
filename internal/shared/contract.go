// Package shared defines the JSON contract exchanged between the Windows agent
// and the backend. Keeping it in one package means the two binaries can never
// drift out of sync (see PLAN.md §2).
package shared

// Sample is one ~1/min observation uploaded by the agent.
//
// Both raw monitor signals are carried verbatim (never a pre-collapsed boolean)
// so the "screen off" rule can be chosen/changed server-side per device without
// redeploying agents (PLAN.md §4.4c). A value of -1 means "not sampled".
type Sample struct {
	ClientTS       int64 `json:"client_ts"`       // unix seconds, agent wall clock (server clamps it)
	MonitorsActive int   `json:"monitors_active"` // raw active-display count (§4.4a); -1 = unknown
	DisplayPower   int   `json:"display_power"`   // raw GUID_SESSION_DISPLAY_STATUS 0/1/2 (§4.4b); -1 = unknown
	// DDCPower is the raw DDC/CI probe result, the validated physical-power-off
	// signal (MONTEST-RESULTS.md): 1 = VCP says on, 2..5 = VCP says standby/off,
	// -1 = no physical-monitor handle, -2 = handle present but query failed,
	// -3 or 0 (absent) = not sampled.
	DDCPower  int    `json:"ddc_power"`
	MonitorOn int    `json:"monitor_on"` // agent's derived 0/1 for convenience; server may recompute
	IsIdle    bool   `json:"is_idle"`
	IdleMS    int64  `json:"idle_ms"`
	Exe       string `json:"exe"`
	Title     string `json:"title"`
}

// MonitorEvent records the exact instant the monitor's on/off state changed
// (PLAN.md §4.5). Integrating these intervals gives an exact monitor-on metric,
// where counting 1/min samples only approximates it to ±60s.
type MonitorEvent struct {
	ClientTS  int64 `json:"client_ts"`
	MonitorOn int   `json:"monitor_on"` // 1 = on, 0 = off
}

// IngestRequest is the body of POST /api/ingest — the agent's single periodic call.
type IngestRequest struct {
	AgentVersion string         `json:"agent_version"`
	AgentBuild   int64          `json:"agent_build"` // monotonic build number, for the update-availability hint
	DeviceUUID   string         `json:"device_uuid"` // Phase 0 identity; replaced by per-device tokens in Phase 3
	Hostname     string         `json:"hostname"`
	ClientNow    int64          `json:"client_now"` // agent wall-clock (unix s) at upload, for clock-skew detection (§8, 0 = not reported)
	Samples      []Sample       `json:"samples"`
	Events       []MonitorEvent `json:"events"`
}

// Manifest describes an agent release. Every field is covered by the signature
// (PLAN.md §5.3 H3): version/build/timestamp/mandatory/url are all bound to the
// binary hash as one signed unit, so a compromised backend cannot mix and match
// (e.g. pair an old signed binary with a spoofed higher version).
type Manifest struct {
	Version   string `json:"version"`
	Build     int64  `json:"build"`     // monotonic; the agent refuses build <= its own
	SHA256    string `json:"sha256"`    // hex sha256 of the agent binary
	Timestamp int64  `json:"timestamp"` // unix seconds, signing time
	Mandatory bool   `json:"mandatory"`
	URL       string `json:"url"` // MUST be server-relative (same-origin), e.g. /agent/download?v=1.2.0
}

// SignedManifest is a Manifest plus its detached Ed25519 signature (hex) over
// the manifest's canonical bytes, verifiable against a key pinned in the agent.
type SignedManifest struct {
	Manifest Manifest `json:"manifest"`
	Sig      string   `json:"sig"`
}

// UpdateInfo is the auto-update block returned on every ingest (PLAN.md §5.1).
// The agent verifies Manifest against its pinned keys and decides for itself;
// Available is only a hint the agent does not trust for the security decision.
type UpdateInfo struct {
	Available bool            `json:"available"`
	Manifest  *SignedManifest `json:"manifest,omitempty"`
}

// IngestResponse is returned by POST /api/ingest.
type IngestResponse struct {
	Accepted   int        `json:"accepted"`    // rows actually inserted (post-dedup/clamp)
	ServerTime int64      `json:"server_time"` // drives agent clock reconciliation (§4.8)
	Update     UpdateInfo `json:"update"`
}

// --- Enrollment & device auth (PLAN.md §6.3, §7.3) -------------------------
//
// Two credential planes stay strictly separate: a one-time enrollment secret
// (minted by the dashboard, consumed once by a new agent) is exchanged on first
// contact for a durable per-device API token. The API token is the bearer
// credential for every /api/ingest call thereafter. Only hashes are stored
// server-side; the plaintext secret/token each cross the wire exactly once.

// PrepareEnrollRequest is the dashboard-side request that mints an enrollment
// token for a new machine. In production this endpoint sits behind Cloudflare
// Access; the agent-facing endpoints below stay public.
type PrepareEnrollRequest struct {
	Name string `json:"name"`
}

// PrepareEnrollResponse carries the one-time secret (shown once) bound to a new
// pending device slot. The installer embeds EnrollSecret in its file body — it
// is never placed in a URL, so it can't leak into edge/proxy logs (§7.3).
type PrepareEnrollResponse struct {
	DeviceUUID   string `json:"device_uuid"`
	EnrollSecret string `json:"enroll_secret"`
	ExpiresIn    int    `json:"expires_in"` // seconds until the secret expires
}

// EnrollRequest is POST /api/enroll (public): a new agent presenting its
// one-time secret to obtain a durable API token.
type EnrollRequest struct {
	EnrollSecret string `json:"enroll_secret"`
	Hostname     string `json:"hostname"`
}

// EnrollResponse returns the durable per-device credentials. APIToken is shown
// exactly once — the server keeps only its hash.
type EnrollResponse struct {
	DeviceUUID      string `json:"device_uuid"`
	APIToken        string `json:"api_token"`
	IngestIntervalS int    `json:"ingest_interval_s"`
}

// DeviceStatus is a device row for the dashboard device list and the "waiting
// for first check-in" enrollment poll. Status is one of: pending (secret minted,
// not yet used), active (enrolled and reporting), expired (secret lapsed unused),
// revoked.
type DeviceStatus struct {
	DeviceUUID     string `json:"device_uuid"`
	Name           string `json:"name"`
	Hostname       string `json:"hostname"`
	Status         string `json:"status"`
	LastSeen       int64  `json:"last_seen"`
	AgentVersion   string `json:"agent_version"`
	AgentBuild     int64  `json:"agent_build"` // monotonic build, for the behind-latest flag (§5.3 M4)
	EnrolledAt     int64  `json:"enrolled_at"`
	LogTitles      bool   `json:"log_titles"`       // false = window titles are dropped (privacy opt-out, §9)
	ClockSkew      int64  `json:"clock_skew"`       // last observed agent-minus-server clock offset, seconds (valid only if ClockSkewKnown)
	ClockSkewKnown bool   `json:"clock_skew_known"` // false = the agent has never reported its wall-clock (old agent / not yet seen)
}

// LatestAgent describes the current published agent release, so the dashboard can
// flag devices running behind it.
type LatestAgent struct {
	Version string `json:"version"`
	Build   int64  `json:"build"`
}

// PatchDeviceRequest updates a device from the dashboard (rename / revoke / title opt-out).
type PatchDeviceRequest struct {
	Name      *string `json:"name,omitempty"`
	Revoked   *bool   `json:"revoked,omitempty"`
	LogTitles *bool   `json:"log_titles,omitempty"`
}
