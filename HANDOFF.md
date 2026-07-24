# FabScreenTime — Session Handoff & Local (Windows) Setup Guide

This file carries the full context of the cloud session over to a local checkout so you can
**run and test the app on real Windows hardware** (which the cloud box couldn't do). Keep it in
the repo; it's committed on branch `claude/screentime-app-plan-nttogj`.

> **If you are Claude continuing this project on Claude Desktop:** read [`PLAN.md`](./PLAN.md)
> (the full development plan — especially **§0**, the key decisions) and this file, then continue
> from **Phase 3** in the roadmap ([PLAN.md §10](./PLAN.md)). The code for Phases 0–2 is built and
> tested; your first high-value job on real hardware is the `montest` validation in section 5 below.

---

## 1. Where things stand

- **Branch:** `claude/screentime-app-plan-nttogj` (NOT merged to a default branch). All work is here.
- **Done & tested (Linux + Windows cross-compile; CI runs the real Win32 code on `windows-latest`):**
  - **Phase 0** — walking skeleton: agent sampling, bounded offline queue, batched idempotent
    ingest with timestamp clamping, per-device summary, no-build placeholder dashboard.
  - **Phase 1** — signed auto-update firebreak: Ed25519-signed manifests, dual pinned keys,
    monotonic anti-rollback, same-origin download enforcement, fail-closed verify-before-swap,
    offline signing CLI (`fst-sign`). **Now enabled** — a real key is pinned (see §6/§7); the
    dev release loop is `scripts/release-local.ps1`.
  - **Phase 2** — monitor signal, transition `monitor_events`, exact-interval monitor-on minutes,
    dirty-days rollup, hidden Scheduled-Task autostart. **Monitor-off detection finalized as the
    DDC/dxva2 signal** (see [MONTEST-RESULTS.md](./MONTEST-RESULTS.md)): the connected-monitor
    count never drops on this fleet, so `ddc_power` is the load-bearing signal.
  - **Phase 3** — auth, enrollment & per-device tokens: one-time enrollment secrets (hashed,
    single-use, 15-min TTL, IP rate-limited), durable per-device bearer tokens (hashed at rest;
    ingest is 401 without a valid one), revocation, DPAPI-encrypted token on the client. Full
    server + agent + credential tests.
  - **Phase 4 — Dashboard MVP** (React 19 + Vite + TypeScript + Tailwind v4, embedded via
    `go:embed`, TanStack Query + react-router): Overview (KPI row, per-device bars, daily trend,
    device list with revoke) and a per-device drilldown (24h monitor-on/input-active timeline,
    top apps, day picker). Monitor-on is the solid primary series, input-active the lighter
    overlay, with the honest "presence proxy, not tamper-proof" note. Light/dark themed, responsive.
    Backend endpoints: `/api/stats/trend`, `/api/devices/{uuid}/timeline`, `.../top-apps`.
    Charts are hand-rolled SVG/divs (fully theme-controlled) rather than Recharts.
  - **Phase 5 — Install-from-website + remaining views**: the dashboard's **Add device** flow
    (name → mint one-time secret → download a personalized silent installer, secret in the file
    body, hash-checked against the manifest → live "waiting… ✓ connected" poll); the
    **signal-comparison** view (monitor-on vs input-active vs *macro* = input-while-screen-off,
    the §0.1 autoclicker fingerprint, flagged in red); the **activity heatmap** (days × hours,
    Tailwind divs); and a per-device **title opt-out** (`log_titles`): the dashboard toggle drops
    window titles **server-side on ingest** so they are never stored. Titles are only ever rendered
    as React text (auto-escaped) — no `dangerouslySetInnerHTML` — so a hostile window title can't
    inject script.
  - **Phase 6 — Production deployment** (see [deploy/RUNBOOK.md](./deploy/RUNBOOK.md)): the
    backend runs on the household **Alpine VM (Proxmox vmid 101, 192.168.1.6)** as its own
    Docker compose project on **port 8090** — `http://192.168.1.6:8090` — with nightly
    `sqlite3 .backup` + rotation via cron, and a **DR restore drill performed and passed**.
    Deployed as a container rather than the plan's Debian-LXC-and-systemd because that host
    already runs the household Docker stack; the static `CGO_ENABLED=0` binary runs fine on
    musl. Verified in production: real agent enrolled and reporting, signed release staged, and
    a stale agent **self-updated 0.0.1 → 0.6.0 against the deployed server**.
- **Not started:** Phases 7–8 (availability/observability → polish), plus the deliberately
  deferred **external reachability** below. See [PLAN.md §10](./PLAN.md).

### Phase 6 caveat — the backend is LAN-only right now

PLAN.md §8.2 calls for a **Cloudflare Tunnel + Cloudflare Access**. That needs your Cloudflare
account (an interactive browser login) and a hostname decision, so it is **not configured**:
roaming agents and off-network phones cannot reach the backend yet, and the dashboard has **no
authentication** — do not expose port 8090 as-is. Options are written up in the runbook.

### One Phase 5 nuance deferred

The title opt-out drops titles **at the server** (never stored), which is the privacy guarantee
that matters. It does **not** yet stop the agent from *sending* titles, so an opted-out device's
titles still traverse the TLS link (visible to the Cloudflare edge) before being dropped. Closing
that gap means returning the preference in the ingest response and having the agent suppress titles
locally — a small, well-scoped follow-up.

### Things that still need YOU / real hardware

1. **`montest` on a second PC (different GPU)** — the last open datapoint (section 5). Not
   blocking; every enrolled machine is calibrated by the probe anyway.
2. **A rotation signing key before real deployment** — only the primary key is pinned. Generate a
   second offline key and add it to `pinnedUpdateKeysHex` (§5.3 H2) so a primary-key compromise is
   recoverable without re-imaging. The private `release.key` must stay offline (it's gitignored).

### One deliberate deferral

The **display-power (DPMS) message-pump watcher** (`GUID_SESSION_DISPLAY_STATUS`) is not
implemented. `display_power` stays "unknown"; `monitor_on` is driven by the DDC/dxva2 signal plus
the connected-monitor count. Adding the pump is a good local task (see section 9).

---

## 2. Prerequisites (Windows)

- **Git** — https://git-scm.com/download/win (includes Git Bash, handy for the `scripts/*.sh`).
- **Go** — install the latest from https://go.dev/dl/ (the module pins `go 1.25.0`; recent Go
  toolchains fetch the matching version automatically). Verify: `go version`.
- **Node 20+** — only needed to **rebuild the dashboard** (`web/`). On Lars's machine it lives at
  `%LOCALAPPDATA%\Programs\nodejs22` (the system PATH may still point at an older Node);
  `scripts/build-ui.ps1` finds it automatically. The built `web/dist` is committed and embedded,
  so building the **backend** never needs Node — `go build ./...` works with the Go toolchain alone.
- **VS Code** (optional) with the Go extension.
- No C compiler needed — every Go dependency is pure Go (`modernc.org/sqlite`, `golang.org/x/sys`).

---

## 3. Get the code locally

In PowerShell:

```powershell
cd $HOME\dev        # or wherever you keep projects
git clone https://github.com/larsenglund/fabscreentime.git
cd fabscreentime
git checkout claude/screentime-app-plan-nttogj
go build ./...      # first build downloads modules; should exit 0
go test ./...       # all tests should pass
```

If `go test ./...` is green, your toolchain is good.

---

## 4. Repo layout

```
PLAN.md                     the full development plan (read §0 first)
HANDOFF.md                  this file
cmd/fabscreentimed/         backend: API + dashboard + SQLite, one binary
cmd/agent/                  Windows agent (stub sampler off-Windows; -once/-install/-uninstall)
cmd/montest/                Phase 0 monitor-signal probe (Windows-only)
cmd/fst-sign/               offline signing CLI: genkey / sign / verify
internal/shared/            JSON contract shared by agent & backend
internal/agent/             agent core, Sampler interface, queue, self-update, autostart
internal/server/            HTTP handlers, SQLite store, rollups, placeholder dashboard
internal/update/            Ed25519 manifest sign/verify (the auto-update firebreak)
scripts/build.sh            builds backend + silent agent.exe + montest.exe + fst-sign (Git Bash)
.github/workflows/ci.yml    CI: Linux build/test + windows-latest smoke run
```

---

## 5. ⭐ FIRST TASK — validate the monitor signal (`montest`)

> **Status: results so far live in [MONTEST-RESULTS.md](./MONTEST-RESULTS.md).** Machine 1
> (GTX 1060; Philips signage over DP, Dell U2515H over DP **and** HDMI) is validated:
> topology/connection signals never fire on physical power-off there. The working signal is
> the probe's dxva2 `ddc_power` column — over DP the physical-monitor handle disappears,
> over HDMI the VCP `0xD6` reply flips to "off"; the combined ON/OFF rule is in the results
> file. **The rule is implemented in the agent** (`ddc_windows.go` watcher → raw `ddc_power`
> per sample → `ddcSaysOff` derivation) and verified end-to-end against the backend: a
> physical power-off flips `monitor_on`, emits the `monitor_events` pair, and lands in the
> daily rollup. Still pending: one probe round on a second PC with a different GPU.

This is the one experiment the whole metric design hinges on (PLAN.md §0.1). Your fleet is a
DisplayPort/HDMI mix, so run it on **one DP machine and one HDMI machine**.

```powershell
# Build the probe (console app; prints to the window and to montest.csv)
go build -o montest.exe .\cmd\montest
.\montest.exe
```

Now, with it running: **physically power the monitor off the way the household normally does, wait
~30 seconds, then power it back on.** Press `Ctrl+C` to stop. Open `montest.csv`.

**What to look for** in the `monitors_active` column while the monitor was off:

- **Drops to 0** (single-monitor) or decreases → connection-presence is a clean, un-fakeable
  screen-off signal on that machine. Set its `monitor_detect_mode = connection` (the default). This
  is the expected DisplayPort result. ✅
- **Stays the same** → that machine (typically HDMI) doesn't expose the power-off via the count.
  It needs the fallback ladder (PLAN.md §4.4c): try DDC/CI, or accept the approximate/heuristic
  mode. Note which machines these are.

Record the result for each connector type — it drives how Phase 2's monitor logic is finalized and
whether the DPMS pump (section 9) is worth adding.

(Multi-monitor shortcut: if powering one screen off makes windows/icons jump to another display,
Windows saw the disconnect — same "connection" conclusion, no CSV needed.)

---

## 6. Run the whole thing locally (backend + agent on the same PC)

**Terminal 1 — backend** (runs fine on Windows; production is Linux/Proxmox):

```powershell
go run .\cmd\fabscreentimed -addr :8080 -db .\dev.db -rollup 30s
# open http://localhost:8080 for the placeholder dashboard
```

**Terminal 2 — agent** (console build so you can watch its logs; the *silent* build is section 8):

```powershell
go run .\cmd\agent -server http://localhost:8080 -interval 5s -datadir .\agentdata
```

Within a few seconds the dashboard at http://localhost:8080 should show your PC with rising
screentime, and `http://localhost:8080/api/dashboard/summary?range=7d` returns JSON. This is the
real Win32 sampler now (real foreground window + exe, real idle, real monitor count) — verify the
active-window/exe and idle columns look right as you use the machine.

---

## 7. Exercise the signed auto-update (Phase 1) locally

This proves the security firebreak on a real machine.

```powershell
# 1) Generate an offline signing key (keep release.key OUT of git; .gitignore covers *.key? add it)
go run .\cmd\fst-sign genkey -out release.key
#    → prints a public key. Copy it.

# 2) Pin it: edit internal/agent/pinnedkeys.go, uncomment a line in pinnedUpdateKeysHex and paste:
#       var pinnedUpdateKeysHex = []string{ "PASTE_PUBLIC_KEY_HEX" }

# 3) Build agent v1 (build number 1) and a "v2" (build number 2)
mkdir dist -Force
go build -ldflags "-X main.Version=2.0.0 -X main.Build=2" -o dist\agent.exe .\cmd\agent

# 4) Sign v2 and stage it for the backend
go run .\cmd\fst-sign sign -key release.key -in dist\agent.exe -version 2.0.0 -build 2 -out dist\manifest.json
go run .\cmd\fst-sign verify -pub PASTE_PUBLIC_KEY_HEX -manifest dist\manifest.json -bin dist\agent.exe

# 5) Run the backend pointed at dist\ as the agent release dir
go run .\cmd\fabscreentimed -addr :8080 -db .\dev.db -agentdir dist

# 6) In another terminal, run a build-1 agent; it should verify+download+swap to 2.0.0 and relaunch
go build -ldflags "-X main.Version=1.0.0 -X main.Build=1" -o run\agent.exe .\cmd\agent
.\run\agent.exe -server http://localhost:8080 -interval 5s -datadir .\agentdata
#    watch the log: "update ... verified; ... update applied; relaunching as 2.0.0"
```

To confirm the firebreak rejects bad updates, re-sign with a *lower* build, tamper `manifest.json`,
or point `url` off-origin — the agent logs a rejection and keeps running v1. (These paths are also
covered by `go test ./internal/agent -run SelfUpdate`.)

> ⚠️ Add `release.key` and `dist/` to `.gitignore` before committing, and **never commit the
> private key**. The public key in `pinnedkeys.go` is fine to commit.

---

## 8. Test silent operation + autostart (Phase 2)

**Silent build** (GUI subsystem — no console window at all):

```powershell
go build -ldflags "-s -w -H=windowsgui -X main.Version=0.1.0 -X main.Build=1" -o dist\agent.exe .\cmd\agent
```

**Install hidden autostart** (copies to `%ProgramData%\FabScreenTime\agent.exe`, registers a
hidden "at logon" Scheduled Task, and starts it):

```powershell
dist\agent.exe -install -server http://localhost:8080
```

Verify it's running and hidden:
- `Get-ScheduledTask -TaskName FabScreenTimeAgent` shows the task (State should reach **Running**).
- Task Scheduler → the task has **Hidden** checked, runs at logon, LIMITED, restart-on-failure.
- Task Manager → Details → `agent.exe` is present; **no** window, **no** tray icon.
- The dashboard shows the device reporting.
- If it doesn't run, read the log: `Get-Content "$env:ProgramData\FabScreenTime\agent.log"` —
  the silent build logs there (added 2026-07-24; the GUI subsystem has no console).

**Install location is `%ProgramData%`, not `%LOCALAPPDATA%` (learned the hard way 2026-07-24).**
A hidden Scheduled Task launching an unsigned exe from the user's `AppData\Local` profile is the
textbook malware-persistence pattern, and Windows' app-reputation heuristics **silently block the
Task-Scheduler launch from there** — the process is never created and *no event is logged*, while
the same exe runs fine interactively and from `%ProgramData%`. A standard user can create the
ProgramData folder without UAC, and the DPAPI token stays per-user, so this is a clean fix, not a
downgrade. Symptom if you ever see it: `schtasks` result `0x80070002` (file-not-found) on an exe
that plainly exists.

**Uninstall:**

```powershell
dist\agent.exe -uninstall
# then remove leftover files if you want a clean machine:
Remove-Item -Recurse -Force "$env:ProgramData\FabScreenTime"
```

### Antivirus / SmartScreen reality (expected)

A hidden, self-persisting, self-updating exe **looks like spyware to Defender/SmartScreen** — that's
inherent, not a bug (PLAN.md §9); the ProgramData install location above is what makes the hidden
autostart actually launch. For real deployment the plan covers code-signing / Trusted-Publisher and
a scoped exclusion. **Do not** disable Defender
globally, and don't install this on any work/school/EDR-managed machine.

---

## 9. Good early local tasks (now that you can run Windows)

1. **Add the DPMS message-pump watcher** (the deferred piece, PLAN.md §4.4b). A message-only window
   + `RegisterPowerSettingNotification(GUID_SESSION_DISPLAY_STATUS)` in a `runtime.LockOSThread`
   goroutine, feeding `display_power` into the sampler and emitting transitions. Now testable: put
   the display to sleep / hit the OS "turn off display" and confirm `display_power` flips. Wire it
   so a pump failure degrades gracefully to the current poll-based path.
2. **Finalize per-device `monitor_detect_mode`** from your `montest` results.
3. **Proceed to Phase 3** — auth, enrollment & per-device tokens (fully testable, no Windows
   specifics): one-time enrollment tokens, hashed per-device bearer tokens, revocation, DPAPI token
   storage on the client, replacing the Phase 0 "trust the device UUID" identity.

---

## 10. Gotchas & notes

- **`go 1.25.0`** is pinned in `go.mod`; install a recent Go and let it fetch the toolchain.
- **SQLite** uses `SetMaxOpenConns(1)` (single writer) — simple and plenty at this scale; don't
  "optimize" it into lock errors.
- **Day boundaries are UTC** for now (rollups). A configurable household timezone is a later
  refinement (PLAN.md §6.2) — fine for testing, note it when you look at daily numbers near midnight.
- **Dashboard is a placeholder** (server-rendered HTML in `internal/server/index.go`); the real
  React/Tailwind/shadcn UI is Phase 4.
- **Backend on Windows vs Linux:** identical code; run it locally on Windows for dev, deploy to the
  Proxmox LXC for real (PLAN.md §8). SQLite/`.db` files are cross-platform.
- **CI** already builds/tests on Linux and runs the real Win32 code on `windows-latest`; keep it
  green (`gofmt`, `go vet`, `go test`).

---

## 11. Git workflow (unchanged)

- Keep developing on **`claude/screentime-app-plan-nttogj`**.
- `go test ./... ; gofmt -l .` before committing (CI enforces both).
- Commit with clear messages; push with `git push -u origin claude/screentime-app-plan-nttogj`.
- No PR yet — the branch isn't merged. Open one only when you decide to.

---

## 12. Quick command reference

```powershell
go build ./...                              # build everything
go test ./...                               # run all tests
gofmt -w .                                  # format
# Backend with the signed agent release served for auto-update:
go run .\cmd\fabscreentimed -db .\dev.db -agentdir agentrelease   # :8080

# Enroll a device: open http://localhost:8080 → "Add device" (mints a one-time
# secret + downloads a silent installer). For a manual/dev enroll without the UI:
go run .\cmd\agent -server http://localhost:8080 -interval 5s -datadir .\agentdata `
  -enroll <one-time-secret-from-/api/enroll/prepare>
# After first enroll the token is saved (DPAPI) in <datadir>\credentials.json; drop -enroll.

# Ship a new client build; running agents self-update within one ingest cycle:
powershell -ExecutionPolicy Bypass -File .\scripts\release-local.ps1 -Version 0.3.0

# Dashboard (web/): rebuild the committed, embedded SPA after editing web/src, then
# rebuild the backend to embed it. For live UI dev, `npm run dev` proxies to :8080.
powershell -ExecutionPolicy Bypass -File .\scripts\build-ui.ps1
cd web ; & "$env:LOCALAPPDATA\Programs\nodejs22\npm.cmd" run dev   # http://localhost:5173

go build -o montest.exe .\cmd\montest ; .\montest.exe   # monitor probe
```
