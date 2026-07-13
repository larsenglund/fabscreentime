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
	ClientTS       int64  `json:"client_ts"`       // unix seconds, agent wall clock (server clamps it)
	MonitorsActive int    `json:"monitors_active"` // raw active-display count (§4.4a); -1 = unknown
	DisplayPower   int    `json:"display_power"`   // raw GUID_SESSION_DISPLAY_STATUS 0/1/2 (§4.4b); -1 = unknown
	MonitorOn      int    `json:"monitor_on"`      // agent's derived 0/1 for convenience; server may recompute
	IsIdle         bool   `json:"is_idle"`
	IdleMS         int64  `json:"idle_ms"`
	Exe            string `json:"exe"`
	Title          string `json:"title"`
}

// IngestRequest is the body of POST /api/ingest — the agent's single periodic call.
type IngestRequest struct {
	AgentVersion string   `json:"agent_version"`
	DeviceUUID   string   `json:"device_uuid"` // Phase 0 identity; replaced by per-device tokens in Phase 3
	Hostname     string   `json:"hostname"`
	Samples      []Sample `json:"samples"`
}

// UpdateInfo is the auto-update block returned on every ingest. Stubbed in
// Phase 0; the signed-manifest fields arrive in Phase 1 (PLAN.md §5).
type UpdateInfo struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
}

// IngestResponse is returned by POST /api/ingest.
type IngestResponse struct {
	Accepted   int        `json:"accepted"`    // rows actually inserted (post-dedup/clamp)
	ServerTime int64      `json:"server_time"` // drives agent clock reconciliation (§4.8)
	Update     UpdateInfo `json:"update"`
}
