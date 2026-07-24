# montest results — §0.1 signal validation on real hardware

Per-machine outcomes of the monitor power-off probe ([`cmd/montest`](./cmd/montest)),
the gating experiment from [PLAN.md §0.1](./PLAN.md). Each machine's result decides its
`monitor_detect_mode` ([PLAN.md §4.4c](./PLAN.md)).

Probe columns: `monitors_active` (`SM_CMONITORS`), `qdc_paths` + `target_available`
(`QueryDisplayConfig(QDC_ONLY_ACTIVE_PATHS)`), `ddc_power` (dxva2: physical-monitor
handle + DDC/CI VCP `0xD6`), `idle_ms` (marks the hands-off window).

## Machine 1 — Lars's PC · Philips 43BDL4650D · DisplayPort (validated 2026-07-24)

Two physical power-button rounds; second round had a clean ~4-minute off window
(18:48:27–18:52:35), unambiguously bracketed by the idle ramp and the DDC transitions.

| Signal | While physically OFF | Verdict |
|---|---|---|
| `monitors_active` (SM_CMONITORS) | stayed `1` | dead |
| `qdc_paths` (QDC active paths) | stayed `1` | dead |
| `target_available` / `statusFlags` | stayed `1` / `0x1` | dead |
| DDC/CI VCP `0xD6` value | never answers off; answers ~2% of polls even when ON | too flaky as a value probe |
| **dxva2 physical-monitor handle presence** | handle **absent for all 248 s** of the off window; present for every on-sample; flipped within seconds of the button in both directions | **works — perfect discriminator in this run** |

**Why connection-mode fails here:** the 43BDL4650D is a signage display; its standby
keeps the DP link/HPD asserted (wake-on-signal), so Windows never removes it from the
topology. The expected "DP drops out" behaviour does not apply to this panel.

**Decision:** `monitor_detect_mode = ddc` for this device, implemented as
**`GetPhysicalMonitorsFromHMONITOR` handle presence** (treat the VCP `0xD6` reply as a
bonus signal when it answers, never as the primary). If OS display-sleep also drops the
handle, that conflation is acceptable: both states mean the screen is dark ⇒ not
screentime.

**Agent follow-up (Phase 2 refinement):** sample physical-monitor-handle count as a raw
per-sample signal (e.g. `phys_monitors`) next to `monitors_active`/`display_power`, on
its own goroutine at a ~2 s cadence with a cached read (DDC transactions are slow and
must not stall the sampler; see the probe's `ddcLoop` for the pattern).

## Machine 2 — HDMI machine · PENDING

Run `dist\montest.exe` (built from the current probe — includes the `ddc_power` column)
on the HDMI machine: power the monitor off ~30 s the household way, hands off input
during the window, power on, Ctrl+C. The startup banner records the connector type; the
CSV answers which rung of the ladder that machine lands on.
