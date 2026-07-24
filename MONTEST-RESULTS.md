# montest results — §0.1 signal validation on real hardware

Per-machine outcomes of the monitor power-off probe ([`cmd/montest`](./cmd/montest)),
the gating experiment from [PLAN.md §0.1](./PLAN.md). Each machine's result decides its
`monitor_detect_mode` ([PLAN.md §4.4c](./PLAN.md)).

Probe columns: `monitors_active` (`SM_CMONITORS`), `qdc_paths` + `target_available`
(`QueryDisplayConfig(QDC_ONLY_ACTIVE_PATHS)`), `ddc_power` (dxva2: physical-monitor
handle + DDC/CI VCP `0xD6`), `idle_ms` (marks the hands-off window).

## Machine 1 — Lars's PC · NVIDIA GTX 1060 6GB (driver 32.0.15.8228, 2026-01) · DisplayPort (validated 2026-07-24)

Tested with **two different DP panels** on the same PC, physical power-button rounds each:

**Round A — Philips 43BDL4650D (signage).** Clean ~4-minute off window (18:48:27–18:52:35),
unambiguously bracketed by the idle ramp and the DDC transitions.

**Round B — Dell U2515H (traditional consumer monitor).** ~35 s off window
(18:59:37–19:00:12), same shape.

| Signal | While physically OFF | Verdict |
|---|---|---|
| `monitors_active` (SM_CMONITORS) | stayed `1` — both panels | dead |
| `qdc_paths` (QDC active paths) | stayed `1` — both panels | dead |
| `target_available` / `statusFlags` | stayed `1` / `0x1` — both panels | dead |
| DDC/CI VCP `0xD6` value | Philips: answers ~2% of polls even when ON (chronic `-2`); Dell: clean `1` when on | works on Dell, too flaky on Philips |
| **dxva2 physical-monitor handle presence** | handle **absent (`-1`) for every off-sample on both panels** (Philips: 248/248 s; Dell: full window after a single ~2 s transitional `-2`); present for every on-sample; flips within seconds of the button in both directions | **works — perfect discriminator on both panels** |

**The §0.1 "DP drops out of the topology" assumption is empirically false on this PC —
even for a consumer Dell DP panel.** For the Philips the likely cause is signage standby
keeping HPD asserted (wake-on-signal). For the Dell the suspect list includes the NVIDIA
driver's monitor/EDID persistence; whether it's the GPU or the panels can only be
separated by testing on a different PC (the HDMI machine will provide that datapoint).
Either way, connection mode cannot be the primary here.

**The uniform per-sample rule that held for both panels:**
`ddc_power != -1` (physical-monitor handle present) ⇒ screen ON;
`ddc_power == -1` (handle absent) ⇒ screen OFF.
The `-2` state (handle present, VCP query failed) must count as **on** — it is the
Philips' chronic on-state and only a ~2 s power-down transition on the Dell. The VCP
value is a bonus signal when it answers (Dell), never the primary.

**Decision:** `monitor_detect_mode = ddc` for this device, implemented as
**`GetPhysicalMonitorsFromHMONITOR` handle presence** per the rule above. If OS
display-sleep also drops the handle, that conflation is acceptable: both states mean
the screen is dark ⇒ not screentime.

**Agent follow-up (Phase 2 refinement):** sample physical-monitor-handle count as a raw
per-sample signal (e.g. `phys_monitors`) next to `monitors_active`/`display_power`, on
its own goroutine at a ~2 s cadence with a cached read (DDC transactions are slow and
must not stall the sampler; see the probe's `ddcLoop` for the pattern).

## Machine 2 — HDMI machine · PENDING

Run `dist\montest.exe` (built from the current probe — includes the `ddc_power` column)
on the HDMI machine: power the monitor off ~30 s the household way, hands off input
during the window, power on, Ctrl+C. The startup banner records the connector type; the
CSV answers which rung of the ladder that machine lands on.
