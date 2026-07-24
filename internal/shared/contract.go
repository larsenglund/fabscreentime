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
