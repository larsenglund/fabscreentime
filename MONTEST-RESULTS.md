# montest results — §0.1 signal validation on real hardware

Per-machine outcomes of the monitor power-off probe ([`cmd/montest`](./cmd/montest)),
the gating experiment from [PLAN.md §0.1](./PLAN.md). Each machine's result decides its
`monitor_detect_mode` ([PLAN.md §4.4c](./PLAN.md)).

Probe columns: `monitors_active` (`SM_CMONITORS`), `qdc_paths` + `target_available`
(`QueryDisplayConfig(QDC_ONLY_ACTIVE_PATHS)`), `ddc_power` (dxva2: physical-monitor
handle + DDC/CI VCP `0xD6`), `idle_ms` (marks the hands-off window).

## Machine 1 — Lars's PC · NVIDIA GTX 1060 6GB (driver 32.0.15.8228, 2026-01) · three rounds, two panels, both connectors (validated 2026-07-24)

Physical power-button rounds, each with a hands-off window cleanly bracketed by the
idle ramp and the `ddc_power` transitions:

| Round | Panel · connector | Topology while OFF (mon/qdc/avail) | `ddc_power` while ON | `ddc_power` while OFF |
|---|---|---|---|---|
| A | Philips 43BDL4650D (signage) · DP | frozen `1/1/1` (~4 min) | `-2` chronic, rare `1` | `-1` all 248 s |
| B | Dell U2515H (consumer) · DP | frozen `1/1/1` (~35 s) | `1` | `-1` (single ~2 s `-2` at power-down) |
| C | Dell U2515H (consumer) · HDMI | frozen `1/1/1` (~83 s) | `1` | **`5`** — handle stays, VCP answers "power off" |

**Finding 1 — connection/topology signals are dead on this PC, all three rounds.** Even
a consumer Dell DP panel never leaves the topology on physical power-off (suspects:
NVIDIA monitor/EDID persistence for the Dell; signage wake-on-signal standby for the
Philips). The §0.1 "DP drops out on power-off" assumption is empirically false here. A
different-GPU PC would be needed to attribute panel vs driver — but that attribution is
moot for the design, because the dxva2 signal works regardless.

**Finding 2 — the dxva2 probe caught every off-window, but the off-signature differs by
connector on the same panel:** over DP the physical-monitor handle disappears (`-1`);
over HDMI the handle stays alive (HDMI +5V keeps the DDC responder powered) and the VCP
`0xD6` reply flips to `5` ("power off"). Detection latency was seconds in both
directions at the probe's 2 s DDC cadence.

**The combined per-sample rule that holds for all three rounds:**

```
screen ON  ⟺ ddc_power == 1  (VCP says on)   OR  ddc_power == -2 (handle present, query failed)
screen OFF ⟺ ddc_power == -1 (no handle)     OR  ddc_power in 2..5 (VCP says standby/off)
```

`-2` must read as ON: it is the Philips' chronic on-state and only a ~2 s power-down
transition on the Dell. (A hypothetical panel whose *off*-state is chronic `-2` would
defeat this rule — exactly what per-machine calibration at enrollment is for.)

**Decision:** `monitor_detect_mode = ddc` for this machine, evaluating the full rule
above (handle presence **and** VCP power value together, never either alone). If OS
display-sleep trips the same signals, that conflation is acceptable: both states mean
the screen is dark ⇒ not screentime.

**Agent follow-up (Phase 2 refinement):** upload the raw `ddc_power` value itself as a
per-sample column (the §4.4c upload-raw-signals principle) next to
`monitors_active`/`display_power`, sampled on its own goroutine at ~2 s with a cached
read (DDC transactions are slow and must never stall the sampler; see the probe's
`ddcLoop` for the pattern). Derive `monitor_on` server-side per device mode.

## Machine 2 — a second PC (different GPU) · PENDING

All three rounds above share one PC/GPU. Running the probe on another household machine
(any connector) adds the missing GPU datapoint and calibrates that device's own mode —
same procedure: copy `dist\montest.exe`, run it, power the monitor off ~30 s hands-off,
power on, Ctrl+C, read the CSV against the rule above.
