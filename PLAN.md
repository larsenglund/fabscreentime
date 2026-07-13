# FabScreenTime — Development Plan

A personal/household screentime tracker for **self-owned Windows PCs**. A tiny, invisible
background agent runs on each machine and, roughly once a minute, records: that the PC is
on, the active foreground window (title + owning `.exe`), whether the user is idle, and
whether the **monitor is on or off**. Every machine reports to a single self-hosted backend
on the owner's Proxmox server, which also serves a responsive dashboard that aggregates
statistics across the whole fleet and lets you enroll a new computer in a couple of clicks.

The guiding constraint everywhere below is **lean, simple, easy to maintain and deploy**:
two Go binaries (agent + backend), one SQLite file, one embedded web UI, and the fewest
moving parts that satisfy the requirements.

> This plan was drafted, then put through four adversarial reviews (simplicity, Windows
> feasibility, security, and missed-improvements). The most important outcomes of that pass
> are collected in **§0**, and the fixes are folded into the sections that follow.

---

## 0. Read this first — key decisions & the one thing to validate before building

### 0.1 CRITICAL: measure monitor **connection/presence**, not display **power state** — because the user physically powers off the monitor

The design leans on: *monitor-on time is a trustworthy proxy for real human screentime because
the household's autoclickers never run with the screen on.* The clarified usage is decisive:
**the autoclicker is started while the screen is on, then the user physically presses the power
button on the monitor**, and the macro keeps running with the monitor physically off. This is
exactly the case where the originally-proposed signal fails, so the primary signal must change.

**Why the original signal (`GUID_SESSION_DISPLAY_STATUS`) does *not* work here.** That API
reports the **OS's logical display-power state** (DPMS), which Windows drives from the power
policy — idle timeout, sleep, or a software "turn off display". **Physically pressing the
monitor's power button is not a DPMS event.** The GPU keeps driving the output, and if the macro
is generating input the idle-off timer never fires either — so the logical state stays `1` (on).
Result: with the original signal, **the autoclicker's monitor-physically-off hours would be
counted as screentime.** The premise breaks.

**The signal that *does* track a physical power-off is monitor connection/presence.** When a
monitor is switched off it can drop its hot-plug-detect (HPD) line, and Windows then removes it
from the display topology — moving windows/icons to another display (or, for a single monitor,
leaving **zero active displays**). The catch is that this is **connection-dependent**:

- **DisplayPort → reliably detectable.** Powering a DP monitor off makes Windows treat it as
  *disconnected*; the display leaves the active topology. This is a well-known (often
  complained-about) DP behaviour — and it's exactly the clean, un-fakeable signal we want. No
  amount of injected input re-asserts HPD, so a `SendInput` macro can't defeat it.
- **HDMI → often *not* detectable.** Many HDMI monitors keep the connection asserted when
  switched off, so Windows still sees them as connected. If the household's monitors are HDMI,
  physical power-off may be invisible to the OS and we need a fallback (below).

**So the primary signal becomes "is the monitor still present in the display topology?"** —
read via `QueryDisplayConfig` (count active paths / connected targets) each sample, plus a
push notification via `WM_DISPLAYCHANGE` and `RegisterDeviceNotification(GUID_DEVINTERFACE_MONITOR)`.
Keep `GUID_SESSION_DISPLAY_STATUS` too, as a **complementary** signal — it still catches the
OS-driven display-off/sleep case. The stored `monitor_on` becomes: *a display is connected
**and** its power state is on*. Either one going off ⇒ screen off.

**Two consequences:**

1. **Drop the "un-fakeable" framing; state the honest dependency.** Whether physical power-off is
   detectable at all depends on the monitor + cable (DP vs HDMI). Present monitor-on in the UI as
   the best available presence proxy with that caveat (see §7).
2. **Validate empirically before building Phase 2 (≈30 lines, ~1 hour) — this is now the gating
   spike.** On **one real household PC**, log *all three* candidate signals once a second —
   `QueryDisplayConfig` active-monitor count, `GUID_SESSION_DISPLAY_STATUS`, and
   `GetLastInputInfo` — and capture: (a) normal use, (b) walk away / idle timeout, (c) **power
   the monitor off exactly the way the household does**, (d) run the household's **actual**
   autoclicker with the monitor off. The decisive question: in (c)/(d), does the **active-monitor
   count drop to 0** (or lose the primary)? If yes → use connection as primary, done. If no
   (HDMI keeps it connected) → fall to the ladder below before building on it.

**Fallback ladder if connection-detection doesn't fire (HDMI/quirky monitor):**
- **DDC/CI power query (VCP `0xD6`)** over I2C — a physically-off monitor usually stops responding
  to DDC, so a failed/`off` query is the signal. Flaky, per-monitor, slow; a fallback, not a
  primary.
- **Change the off-method** — if acceptable, having the user hit a "turn off display" hotkey
  (software DPMS-off) instead of the physical button makes `GUID_SESSION_DISPLAY_STATUS` fire
  cleanly. Changes a habit, so only if the above fail.
- **Accept the limitation** — count `monitor_on AND active` as "confidently present" and treat a
  known game running with perfectly regular input as "likely macro", surfaced as a separate
  bucket rather than trusted screentime.

Everything downstream (schema stores every signal raw; UI leads with monitor-on but shows
input-active as a second layer; "confidently present" = `monitor_on AND active`, "likely
macro/unattended" = `monitor_off` or `monitor_on AND idle`) is designed to stay correct
regardless of how that validation lands.

### 0.2 Decision A — keep silent auto-update (recommended, with a rollout gate)

You explicitly asked for silent, automatic self-update. Keep it — **but understand the
trade-off the reviews flagged:** a fleet of invisible, auto-updating agents that fetch and
execute code from a server is *architecturally identical to botnet command-and-control*. The
security cost is not optional decoration; it is the price of the feature: an offline signing
key, a public key pinned in the agent, signed manifests, monotonic versioning, and
verify-before-swap (§5). If you were willing to give up **silent** updates (push a new agent a
few times a year over RDP / re-run the installer, exactly as you deploy the backend), you
could delete that entire subsystem and shrink the threat model to something a solo maintainer
reasons about trivially. That is the single biggest possible simplification.

**Recommendation:** keep auto-update (it's a stated requirement and roaming laptops make manual
updates a real chore), but add an **owner-gated / canary rollout** (§5.4) so publishing a build
doesn't instantly roll the whole fleet. This keeps the convenience while capping the blast
radius of *your own* bad-but-valid build — which is the far more likely failure than an
attacker.

### 0.3 Decision B — keep the React dashboard (justified by "modern, trimmed UI")

The lightest possible dashboard is Go `html/template` + a sprinkle of htmx and one vendored
chart file — no npm, one toolchain. The simplicity review is right that
React + Vite + Tailwind + shadcn + Recharts is the highest-churn, second-toolchain part of the
whole project. **But** your explicit ask — a *modern, trimmed UI built with Claude's design
help, great on mobile and desktop* — is exactly what that stack is best at, and what Claude
generates most fluently. **Recommendation:** keep React + Tailwind + shadcn, and treat it as
the *one* place we deliberately spend complexity. If maintenance burden ever bites, the escape
hatch (server-rendered HTML + htmx) is real and documented.

### 0.4 Simplifications adopted from the review (folded into the sections below)

- **Backups:** drop the Litestream daemon; rely on Proxmox `vzdump`/PBS nightly snapshots +
  a one-line nightly `sqlite3 .backup` cron. (§8.4)
- **Reverse proxy:** drop the extra Caddy hop by default; `cloudflared` → `localhost:8080`
  directly. (§8.3)
- **Dashboard auth:** put the dashboard behind **Cloudflare Access** (one email policy)
  instead of building a users table + password + session plane. (§7.4)
- **Router:** Go 1.22+ `net/http.ServeMux` (method + path-param routing) instead of `chi`. (§6)
- **One overlay network** (Cloudflare), not Cloudflare *and* Tailscale. (§8.2)
- **Defer** raw-sample purge + `VACUUM` until the DB is actually large (year 2+); keep the
  nightly rollup. (§6.2)

---

## 1. Overview & Goals

**Concept.** One invisible per-PC agent → one self-hosted backend on Proxmox → one responsive
dashboard. The agent samples four signals once a minute, buffers them to disk, and uploads
batches. The backend stores them in SQLite, rolls them into permanent daily aggregates, and
serves both the JSON API and the embedded web UI off a single port. Enrolling a new machine is
a couple of clicks on the website.

**The load-bearing metric — monitor-on minutes (with the honesty from §0.1).** Input-idle
(`GetLastInputInfo`) is a *secondary* signal because synthetic input from a macro can be
indistinguishable from real hardware input at that API. Monitor power state is the *primary*
signal because, for the household's actual macro behaviour, it excludes macro time — validated
by the §0.1 spike. The schema stores **both** signals raw so the metric can be recomputed or
re-tuned server-side without ever redeploying agents.

**Overarching goal.** Lean and low-maintenance: two static Go binaries, one SQLite file, one
`cloudflared` tunnel, `vzdump` for backups. The agent and backend share one language and one
JSON-contract package, so there is one repo, one build, one data model.

---

## 2. High-Level Architecture

```
                       Cloudflare edge (public HTTPS, DNS)
                                     │  outbound-only tunnel (no open ports)
   ┌──────────────────────────────── │ ─────────────────────────────────────┐
   │ Proxmox host                     ▼                                       │
   │   ┌─────────────────────────────────────────────────────────────────┐   │
   │   │ Unprivileged Debian 12 LXC (non-root service user)              │   │
   │   │   • cloudflared     (systemd)                                   │   │
   │   │   • fabscreentimed  — ONE Go binary (systemd) ──────────────┐   │   │
   │   │        GET  /             → embedded SPA (React/Vite dist)   │   │   │
   │   │        POST /api/ingest   → samples upload + update-check    │   │   │
   │   │        POST /api/enroll   → self-enrollment (public)         │   │   │
   │   │        GET  /api/*         → dashboard aggregates            │   │   │
   │   │        GET  /agent/*       → SIGNED agent manifest + .exe    │   │   │
   │   │        GET  /healthz       → liveness (dead-man's switch)    │   │   │
   │   │        SQLite (WAL)  /var/lib/fabscreentime/data.db          │   │   │
   │   │   • nightly cron: sqlite3 .backup → NAS                      │   │   │
   │   └─────────────────────────────────────────────────────────────┘   │   │
   │   Proxmox vzdump/PBS nightly snapshot of the whole LXC               │   │
   └─────────────────────────────────────────────────────────────────────┘   │
          ▲                                             ▲
          │ HTTPS POST /api/ingest ~60s                 │ HTTPS dashboard
          │ (samples + version → update block)          │ (Cloudflare Access)
   ┌──────┴───────────────┐                       ┌─────┴────────┐
   │ Windows agent (Go)   │  one per enrolled PC  │ Owner phone  │
   │ hidden, GUI-subsystem│                       │ / laptop     │
   │ per-user session     │                       └──────────────┘
   └──────────────────────┘
```

**Three components, one backend artifact.** The agent's entire network life is *one endpoint,
once a minute*; a request arriving is itself the liveness signal, and its absence marks the
device offline (and triggers an alert — §7.5). The backend is a single static binary that
serves the API, the embedded SPA, and the signed agent artifacts. The dashboard is a React SPA
compiled to static files and embedded via `go:embed`.

---

## 3. Recommended Tech Stack

One opinionated choice per layer, biased to lean/simple/easy-deploy.

| Layer | Choice | Why |
|---|---|---|
| Agent + backend language | **Go** | One toolchain builds the console-less Windows agent (`GOOS=windows`, `-H=windowsgui`) *and* the static Linux backend; shared structs for the JSON contract. |
| Windows API access | **`golang.org/x/sys/windows`** | Clean syscall wrappers for every API needed; no cgo. |
| Agent self-update | **`minio/selfupdate`** | Atomic rename-swap + rollback on Windows, with hash/signature verification. |
| Update signing | **minisign / Ed25519** (`aead.dev/minisign`, pure Go) | Offline key; **two** public keys pinned in the agent (primary + rotation). |
| Backend router | **stdlib `net/http.ServeMux`** (Go 1.22+) | Method + path-param routing without a dependency. |
| Database | **SQLite (WAL)** via **`modernc.org/sqlite`** (pure Go) | 1 row/min/machine is orders of magnitude below its ceiling; single-writer fits; clean cross-compile with `CGO_ENABLED=0`. |
| DB backup | **`vzdump`/PBS snapshot + nightly `sqlite3 .backup` cron → NAS** | No extra daemon; RPO of a day is fine for screentime stats. |
| SPA framework | **React 19 + Vite + TypeScript** | Builds to a static `dist/` embedded in the Go binary. |
| UI components | **Tailwind CSS v4 + shadcn/ui** | Copy-in components, first-class dark mode, the vocabulary Claude generates best. |
| Charts | **Recharts** (via shadcn Chart wrapper) | Themed by the same CSS variables as the UI; ample for pre-aggregated data. |
| SPA data layer | **TanStack Query + react-router + `fetch`** | Read-mostly dashboard; no global-state ceremony. |
| Backend host | **Unprivileged Debian 12 LXC + systemd** | Boots in ~1s, idles at tens of MB, `vzdump`-snapshots trivially; the binary *is* the container — no Docker daemon. |
| Reachability | **Cloudflare Tunnel (`cloudflared`)** | Public HTTPS for roaming agents + phone; no open ports, no exposed home IP. |
| Dashboard auth | **Cloudflare Access** (email policy) | Deletes the entire password/session plane; agent tokens stay separate. |
| CI/release | **GitHub Actions**: build + `windows-latest` smoke-run + `govulncheck` + offline-signed release | Minimal, but actually *runs* the agent and scans deps. |

---

## 4. The Windows Agent

### 4.1 Structure for testability (do this at Phase 0 — cheap now, painful later)

Put **all** hard logic behind a platform-neutral core driven by a `Sampler` interface:

```go
type Reading struct {
    TS            int64  // filled by the core, not the sampler
    MonitorsActive int   // active-display count (primary presence signal, §4.4a)
    DisplayPower  int    // GUID_SESSION_DISPLAY_STATUS: 0 off, 1 on, 2 dimmed (§4.4b)
    IsIdle        bool
    IdleMS        int64
    ExeName       string
    WindowTitle   string
}
// stored monitor_on = (MonitorsActive > 0) AND (DisplayPower != 0); both raw fields kept.
type Sampler interface { Sample() (Reading, error) }
```

- `sampler_windows.go` — real Win32 syscalls.
- `sampler_stub.go` (`//go:build !windows`) — returns fakes so the whole agent **builds and
  unit-tests on Linux/CI**.

Buffering, batching, backoff, clock reconciliation, version compare, and the self-update
decision all live in this neutral core and are table-tested against a fake sampler. Add a
`windows-latest` CI job that actually **runs** the agent for ~10s and asserts it produces
samples — otherwise 100% of the hard logic is only ever exercised by hand on a physical PC.

### 4.2 Foreground window + owning exe

```
HWND h = GetForegroundWindow();                      // NULL = no foreground / locked / secure desktop
GetWindowTextW(h, ...);                               // UTF-16 title
GetWindowThreadProcessId(h, &pid);
OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, ...);  // limited right = far fewer access-denied failures
QueryFullProcessImageNameW(...);                      // full exe path
```

- Use `PROCESS_QUERY_LIMITED_INFORMATION`; it is grantable far more often than full query
  rights and is what `QueryFullProcessImageNameW` is designed for.
- **`GetForegroundWindow() == NULL`** is a distinct "no foreground / locked" state (UAC, lock
  screen, secure desktop) — a useful signal, not an error.
- **UWP/packaged apps** report `ApplicationFrameHost.exe` as the exe. If the basename is
  `ApplicationFrameHost.exe`, fall back to the window **title** as the identity — plenty for a
  tracker; don't over-engineer child-window walking.
- **`OpenProcess`/`QueryFullProcessImageNameW` can still fail** (`ERROR_ACCESS_DENIED`) for
  elevated/high-integrity or protected foreground processes (anti-cheat, DRM, some AV). On any
  failure, record `exe = unknown` and fall back to the title — **never drop the sample.**
- Log titles/exes **raw**; do all categorization server-side (re-classify history without
  redeploying agents). Convert UTF-16 → UTF-8 at the edge.

### 4.3 Idle detection (secondary signal) — get the tick math right

```
GetLastInputInfo(&lii);                       // lii.dwTime is 32-bit, from GetTickCount (NOT 64-bit)
idleMs = (uint32)GetTickCount() - lii.dwTime; // do the subtraction in 32-bit UNSIGNED space
idle   = idleMs > 60_000;                      // 60s threshold matches the cadence
```

> **Correction from the feasibility review:** do **not** compute
> `GetTickCount64() - lii.dwTime`. `dwTime` is a 32-bit `GetTickCount` value; mixing it with a
> 64-bit "now" produces garbage after ~49.7 days of uptime. The correct idiom is a 32-bit
> **unsigned** subtraction, whose natural wraparound yields the right delta across the boundary.

Store `idle_ms` **raw** (not just the boolean) so the idle threshold can be re-tuned
server-side — this requires an `idle_ms` column in `samples` (§6.2).

### 4.4 Monitor on/off — the primary signal (connection/presence first, power state second)

Per §0.1, the household **physically powers the monitor off**, so the primary signal is monitor
**connection/presence**, with display **power state** as a complementary second signal. Both are
read/maintained on one **message-only window** (`CreateWindowExW` with `HWND_MESSAGE` — no UI, no
taskbar entry), pumped by a dedicated message-loop goroutine.

**(a) Connection/presence — the primary signal for a physically powered-off monitor.**

```
// Sampled read: how many displays are actively connected right now?
QueryDisplayConfig(QDC_ONLY_ACTIVE_PATHS, ...);   // count active paths / connected targets
//   (EnumDisplayDevices with DISPLAY_DEVICE_ACTIVE, or GetSystemMetrics(SM_CMONITORS), also work)
// Push updates so we don't miss transitions between samples:
//   WndProc handles WM_DISPLAYCHANGE, and
RegisterDeviceNotification(msgWin, &GUID_DEVINTERFACE_MONITOR, DEVICE_NOTIFY_WINDOW_HANDLE);
//   → on DBT_DEVICEARRIVAL / DBT_DEVICEREMOVECOMPLETE, re-query and cache the active count.
```

A DisplayPort monitor powered off drops out of the active topology (active count falls, to **0**
for a single-monitor PC) — that's the clean signal we want, and injected input cannot re-assert
it. **Whether this fires for the household's monitors is the gating unknown (DP yes, HDMI often
no) and is what the §0.1 / Phase-0 spike measures.**

**(b) Display power state — complementary, catches OS-driven display-off/sleep.**

```
RegisterPowerSettingNotification(msgWin, &GUID_SESSION_DISPLAY_STATUS, DEVICE_NOTIFY_WINDOW_HANDLE);
// WndProc: on WM_POWERBROADCAST + PBT_POWERSETTINGCHANGE,
//   read POWERBROADCAST_SETTING.Data[0] as DWORD: 0=off, 1=on, 2=dimmed. Cache it (atomic/mutex).
```

Stored `monitor_on` = **(a display is connected) AND (its power state is on)**; either going off
⇒ screen off.

> **Correction from the feasibility review:** `WM_POWERBROADCAST` / `WM_DISPLAYCHANGE` only arrive
> if a thread is **running a message loop** (`GetMessage`/`TranslateMessage`/`DispatchMessage`).
> If the sampler just `time.Sleep`s for 60s and reads a cached value, nothing pumps messages and
> the cache freezes forever. **Dedicate a goroutine with `runtime.LockOSThread()`** that creates
> the window and runs a blocking message loop, updating atomic caches; the sampler reads those
> caches. Never create the window on a thread that then sleeps.

Details:
- `RegisterPowerSettingNotification` delivers an immediate callback with the current value; also
  seed the connected-count from an initial `QueryDisplayConfig`. **Re-check both on resume**
  (`PBT_APM_RESUMEAUTOMATIC` / `RESUMESUSPEND`) — caches can be stale right after sleep.
- Treat power-state `2 = dimmed` as **on** for presence, but weight it weakly (dim = approaching
  the idle timeout, i.e. weak evidence of *absence*, not presence).
- **RDP / disconnect / fast-user-switch staleness:** a per-session cache can get stuck at "on"
  when a session is disconnected (RDP drop, fast-user-switch away) because notifications stop.
  Wire `WTSRegisterSessionNotification` (same window) and, on `WTS_CONSOLE_DISCONNECT` /
  `WTS_REMOTE_DISCONNECT` / `WTS_SESSION_LOCK` / `WTS_SESSION_LOGOFF`, force monitor state to
  **off/unknown**. Only count monitor-on while your session is the **active console session**
  (`WTSGetActiveConsoleSessionId`) — this also prevents two simultaneous sessions from both
  claiming the one physical monitor (§4.7).
- **HDMI fallback (only if the spike shows connection doesn't drop):** probe DDC/CI power mode
  (VCP `0xD6`) — a physically-off monitor typically stops answering DDC, so a failed/`off` query
  becomes the signal. Flaky and per-monitor; a fallback, not the primary (see §0.1 ladder).

### 4.5 Exact minutes via transition events (not just 1/min snapshots)

Counting `COUNT(*)` snapshots as "minutes" quantizes the headline metric to ±60s and
mis-attributes the flip minute. Since monitor state is already push-based, **also emit a
transition event** `(ts, monitor_on)` whenever it changes. Then:

- **monitor-minutes = exact integral of on-intervals** from transition events (precise);
- 1/min snapshots become the liveness + foreground-app channel.

This makes the one metric everything depends on exact instead of rounded, reusing plumbing you
already have.

### 4.6 Silent operation (no window, no tray, no console)

Three independent visibility sources — kill all three:

1. **Console flash** — fixed only at **compile time**: `go build -ldflags "-s -w -H=windowsgui"`.
   A console-subsystem binary allocates a console before `main()` runs, so runtime `FreeConsole`
   still flashes. GUI subsystem = no console, ever.
2. **App window** — the only window is the hidden `HWND_MESSAGE` window; never shown, never on
   the taskbar.
3. **Tray icon** — simply never call `Shell_NotifyIcon`.

Log to a file under `%LOCALAPPDATA%\FabScreenTime\`. The process appears only in Task Manager's
Details tab — correct for self-owned monitoring: invisible to a *normal user*, always findable
and removable by an *administrator*.

### 4.7 Multi-user machines & per-person attribution

Per-user installs on a shared family PC raise two issues:

1. **Attribution** — add a `user_sid` / username to the sample/device model and decide
   explicitly whether a "device" is a *machine* or a *(machine, user)*. Recommended: device =
   machine, and tag samples with the user so the dashboard can split by person.
2. **Double-counting** — with Fast User Switching, *both* sessions' agents receive
   display-status and would both report `monitor_on = 1` for the *one* physical monitor. Gate
   monitor-on counting on "am I the active console session" (§4.4) so household totals don't
   silently 2×.

### 4.8 Local state, queue, and credential storage

- **Bounded disk queue.** The offline buffer must have a cap (e.g. ring buffer, last ~7 days,
  drop-oldest) so a week-long backend outage can't grow it unbounded.
- **Encrypt the device token at rest** with DPAPI (`CryptProtectData`, per-user) under
  `%LOCALAPPDATA%` — never plaintext, so another user on a shared box can't read/impersonate it.
- **Clock reconciliation.** Record both a monotonic clock and wall clock; use the `server_time`
  returned by `/api/ingest` (§6.3) to compute and persist a per-device clock offset. A dead
  CMOS battery or a backward NTP step would otherwise file data into the wrong day *and* silently
  drop real samples on `(device_id, ts)` PK collisions. Correct `ts` before upload (or send
  `client_ts` + `skew` and let the server correct), and flag devices whose skew exceeds a
  threshold on the dashboard.

### 4.9 Autostart (hidden, survives reboot)

A **hidden Scheduled Task, trigger "At log on"** of the target user (more robust than the HKCU
Run key). Register it via the Task Scheduler COM API / XML with:

- `Hidden = true`, **"Run only when user is logged on"** (interactive session — never
  SYSTEM/session 0, which would reintroduce the monitor-state problem),
- integrity `LIMITED` (no elevation at runtime — keeps EDR quieter),
- `RestartOnFailure`, `ExecutionTimeLimit = PT0S`,
- `DisallowStartIfOnBatteries = false`, `StopIfGoingOnBatteries = false` (laptop-safe),
- `MultipleInstances = IgnoreNew`, plus a **named mutex** single-instance guard in the agent
  (the task setting only governs task-triggered starts, not the self-update relaunch — §5.2).

Install target: `%LOCALAPPDATA%\FabScreenTime\agent.exe` (per-user, no UAC for the copy).

---

## 5. Auto-Update Mechanism

### 5.1 Flow (piggybacked on the once-a-minute upload — no extra requests)

1. Every ~60s the agent `POST`s its sample batch to `/api/ingest` **including its current
   version**.
2. The response includes an `update` block whose *security-critical fields come from a signed
   manifest* (§5.3), not from free-form response JSON.
3. If a newer version is offered (and the rollout gate allows it — §5.4), the agent downloads
   the new exe, **verifies it against a pinned key before touching anything**, swaps, and
   relaunches.

### 5.2 Self-replacement mechanics (Windows can't overwrite a running exe)

Windows locks a running image but permits **renaming** it. The standard, fail-closed dance
(implemented by `minio/selfupdate`, with rollback):

```
1. verify signature + SHA-256 of downloaded bytes   ← BEFORE touching agent.exe (fail-closed)
2. rename  agent.exe → agent.exe.old                (allowed while running)
3. write   new bytes → agent.exe
4. relaunch agent.exe (new); current process exits
5. next startup: delete agent.exe.old once unlocked; keep one cycle for rollback
```

Hardening (feasibility review):
- **Named-mutex single-instance guard** — the relaunched child is spawned by the old process
  (`CreateProcess`), so it isn't the Task-Scheduler-tracked instance; a later task trigger + the
  relaunched child could coexist without the mutex.
- **Retry the relaunch with backoff** — real-time AV often holds a transient lock on the
  freshly-written exe, so the immediate `CreateProcess` can hit `ERROR_SHARING_VIOLATION`.
- No separate updater helper is needed — the rename-self trick removes the reason for one.

### 5.3 The signed-update security model — a HARD REQUIREMENT

A silent, invisible, auto-updating agent that executes server-fetched code is structurally
identical to botnet C2. The one property that separates "tracker" from "botnet": **a fully
compromised backend must not be able to run arbitrary code on the enrolled PCs.** This is built
**first** (Phase 1), before a second machine is ever enrolled.

1. **Signed manifest + binary, verified against a pinned key (the load-bearing control).**
   Maintain an **offline Ed25519 signing key (minisign) that never touches the Proxmox server**
   — ideally on a **hardware token** (YubiKey/PIV) so a compromised dev machine can't exfiltrate
   it. The build produces a manifest, signed offline. The agent verifies signature + hash
   **before doing anything** with the bytes. A popped backend can serve whatever it wants; the
   agent rejects anything not signed by the offline key. TLS alone does **not** give you this.
2. **Sign the right bytes (security review H3).** Sign **one canonical serialized blob**
   containing `{version, monotonic_build, sha256, timestamp, mandatory}` *together*. Put
   version/build/timestamp in minisign's **trusted comment** (which is signed) — **never** the
   untrusted comment. The agent must, atomically: verify sig with a pinned key →
   `monotonic_build > stored_build` → downloaded-bytes SHA-256 == manifest.sha256 — all bound to
   the same signed object, all **before** the rename-swap. This closes the classic
   field-substitution/rollback bypass where an unsigned version field is paired with an
   old signed binary.
3. **Dual pinned keys for recoverable key rotation (security review H2).** Pin **two**
   independent public keys (primary + an offline rotation/backup key on separate media) and
   accept manifests signed by either. A primary-key compromise is then recoverable by signing a
   "new pinned key" transition with the backup. Store the backup encrypted and off-site.
   Document that with no rotation key, key theft = re-image the fleet.
4. **Constrain backend-controlled behaviour fields (security review M4).** `mandatory` and
   `version` live **inside the signed manifest**. The agent constrains the download `url` to
   **same-origin** (so a popped backend can't point agents off-domain to beacon their IPs), and
   treats fleet-wide destructive commands (mass self-uninstall / `410`) as requiring more than a
   single unsigned response field. Alert the owner when a device hasn't updated in N days (the
   "freeze the fleet on a vulnerable version" case).
5. **First-install boundary (security review H1).** Pinning protects *updates*, not the *first*
   binary a new machine runs. Distribute the **initial installer out-of-band** (attach it, with
   its minisign signature, to the GitHub Release) rather than trusting the possibly-compromised
   backend for the first executable; have the "Add a device" page link the signature to verify.
   Otherwise a popped backend achieves RCE at each new enrollment even though the existing fleet
   is safe.
6. **Fail-closed & tamper-evident.** Any signature/hash/download failure → discard, keep running
   the current version, log it, upload an error event. Every self-update is logged old→new; the
   dashboard shows agent-version-per-machine so an unexpected or missing update is visible.

**Net effect:** worst case for a compromised backend drops from "instant fleet RCE" to "stops
serving updates" (blocked), "replays a currently-valid build" (blocked by monotonicity), or
"leaks data" (bad, recoverable) — but not fleet ownership.

### 5.4 Availability guardrails (your own bad build is the likelier failure)

- **Owner-gated / canary rollout.** Publishing a signed manifest should **not** instantly roll
  the fleet. Add a dashboard toggle ("approve v1.5") and/or a canary: one device updates first
  and must report healthy before the manifest opens to the rest. The signing model is untouched;
  this only controls *when* the already-verified build is offered.
- **Auto-rollback on a crash-looping update.** `RestartOnFailure` will happily relaunch a
  valid-but-broken build forever. Keep a crash-counter: on startup, if this version crashed ≥3×
  within M minutes **before a successful check-in**, restore `agent.exe.old` and pin to it. Gate
  a new version as "committed" only after it checks in once post-update; otherwise roll back.

### 5.5 Complete uninstall

An explicit `--uninstall` path (and the `410`/revoke self-uninstall) must remove the Scheduled
Task, the `%LOCALAPPDATA%\FabScreenTime\` files (rename-self problem — schedule deletion or spawn
`cmd /c del` on exit), and log the machine-wide leftovers that revocation *won't* clean (any
Defender exclusion / Trusted-Publisher cert), so cleanup is honest.

---

## 6. Backend & Data Model

### 6.1 Shape

One Go binary, one port: embedded SPA at `/`, JSON API at `/api/*`, signed agent artifacts at
`/agent/*`, `/healthz` for liveness, one SQLite file with WAL. Open pragmas:
`journal_mode=WAL`, `busy_timeout=5000`, `synchronous=NORMAL`, `foreign_keys=ON`,
`auto_vacuum=INCREMENTAL`.

**Volume math (why SQLite, not Postgres/TSDB):** 1 sample/machine/minute = 525,600 rows/machine/
year; 10 machines ≈ 5.3M rows/year ≈ ~0.6 GB/year raw. With permanent title-free rollups the
long-term size is trivial. A single-writer embedded DB is the right fit; a second daemon would
add maintenance for zero benefit.

### 6.2 Schema

```sql
CREATE TABLE devices (
    id             INTEGER PRIMARY KEY,
    device_uuid    TEXT NOT NULL UNIQUE,
    name           TEXT NOT NULL,               -- editable in UI
    api_token_hash TEXT NOT NULL,               -- SHA-256 of per-device ingest token
    enrolled_at    INTEGER NOT NULL,
    enroll_ip      TEXT,                         -- true client IP at enrollment (audit)
    last_seen      INTEGER,                      -- updated every ingest (liveness)
    agent_version  TEXT,
    clock_skew_s   INTEGER,                      -- last observed device clock offset
    os_info        TEXT,
    log_titles     INTEGER NOT NULL DEFAULT 1,   -- per-device title opt-out (§9)
    revoked        INTEGER NOT NULL DEFAULT 0
);

-- Raw ~1/min observations (source of truth). Retain ~90d (purge deferred; see note).
CREATE TABLE samples (
    device_id    INTEGER NOT NULL REFERENCES devices(id),
    user_name    TEXT,                            -- per-person attribution on shared PCs
    ts           INTEGER NOT NULL,                -- unix seconds, server-clamped (§6.3)
    monitor_on   INTEGER NOT NULL,                -- derived: monitors_active>0 AND display_power!=0 ← PRIMARY
    monitors_active INTEGER,                      -- raw active-display count (§4.4a) — the physical-off signal
    display_power   INTEGER,                      -- raw GUID_SESSION_DISPLAY_STATUS 0/1/2 (§4.4b)
    is_idle      INTEGER NOT NULL,                -- ← SECONDARY signal
    idle_ms      INTEGER,                         -- raw, so the threshold can be re-tuned
    exe_name     TEXT,
    window_title TEXT,
    PRIMARY KEY (device_id, ts)                   -- idempotent ingest: dedups retried batches
) WITHOUT ROWID;
CREATE INDEX idx_samples_ts ON samples(ts);

-- Exact monitor-on intervals (§4.5) for a precise headline metric.
CREATE TABLE monitor_events (
    device_id  INTEGER NOT NULL REFERENCES devices(id),
    ts         INTEGER NOT NULL,
    monitor_on INTEGER NOT NULL,
    PRIMARY KEY (device_id, ts)
) WITHOUT ROWID;

-- Permanent per-device/day rollup (survives raw purge; NO titles).
CREATE TABLE daily_stats (
    device_id       INTEGER NOT NULL REFERENCES devices(id),
    day             TEXT NOT NULL,                -- 'YYYY-MM-DD', household TZ
    monitor_minutes INTEGER NOT NULL DEFAULT 0,   -- PRIMARY headline metric (from monitor_events)
    active_minutes  INTEGER NOT NULL DEFAULT 0,   -- monitor_on AND NOT is_idle
    session_minutes INTEGER NOT NULL DEFAULT 0,   -- any sample present = a user session was active
    PRIMARY KEY (device_id, day)
);

-- Permanent per-device/day/app rollup (top apps; NO titles).
CREATE TABLE daily_app_stats (
    device_id       INTEGER NOT NULL REFERENCES devices(id),
    day             TEXT NOT NULL,
    exe_name        TEXT NOT NULL,
    monitor_minutes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (device_id, day, exe_name)
);
```

Notes:
- **`session_minutes`, not `powered_minutes`.** Because the agent runs "only when a user is
  logged on," no samples are produced at the lock/login screen or when the PC is on with nobody
  logged in — so the honest name is *session-active minutes*, and sleep vs shutdown are
  indistinguishable (both = absence of samples). Documented, not pretended.
- **Dashboard auth is Cloudflare Access, so there's no `users` table** (Decision B/§0.4).
- **Purge deferred.** Keep the nightly rollup; **defer** `DELETE FROM samples WHERE ts<now-90d`
  + vacuuming until the DB is actually large (year 2+). When you do purge, use
  `PRAGMA incremental_vacuum`, not full `VACUUM` (a full vacuum takes a whole-DB write lock and
  forces a full re-snapshot of backups).

**The primary metric in SQL** — precise, from intervals; approximate fallback from snapshots:

```sql
-- Precise monitor-on seconds = integral of on-intervals from monitor_events (preferred).
-- Approximate fallback (each present sample ≈ 1 minute):
SELECT COUNT(*) FROM samples WHERE device_id=? AND monitor_on=1 AND ts>=? AND ts<?;
-- Secondary "engaged" view (only trustworthy WHEN monitor is on):
SELECT COUNT(*) FROM samples WHERE device_id=? AND monitor_on=1 AND is_idle=0 AND ts>=? AND ts<?;
```

### 6.3 Endpoints

Two **strictly separate** auth realms: agents use a per-device bearer token (only its SHA-256
hash is stored); humans reach the dashboard via **Cloudflare Access**. Dashboard endpoints never
accept a device token and vice-versa.

```
POST /api/enroll                     ← public (roaming agents)
  { "name": "Living room PC", "enroll_secret": "<one-time ≥128-bit token, in BODY only>" }
  → 201 { "device_uuid", "api_token" (shown once), "ingest_interval_s": 60 }

POST /api/ingest                     ← the hot path (ingest + update-check in one call)
  Authorization: Bearer <device-token>
  { "agent_version": "1.4.2",
    "samples": [ {"client_ts":…, "monitor_on":1, "is_idle":0, "idle_ms":…, "exe":"chrome.exe", "title":"…"}, … ],
    "events":  [ {"client_ts":…, "monitor_on":0}, … ] }
  → 200 { "accepted": 2, "server_time": …,          // server_time drives clock reconciliation
          "update": { … from the SIGNED manifest, gated by rollout … } }

GET   /agent/manifest                → signed manifest blob {version, monotonic_build, sha256, timestamp, mandatory}
GET   /agent/download?v=…            → signed agent .exe bytes
GET   /healthz                       → 200 (systemd/cloudflared/dead-man's switch)

GET   /api/dashboard/summary?range=7d
GET   /api/devices
GET   /api/devices/:uuid/timeline?day=YYYY-MM-DD
GET   /api/devices/:uuid/top-apps?range=30d&limit=10
GET   /api/stats/trend?range=90d&group=day
PATCH /api/devices/:uuid             → rename / revoke / toggle log_titles / approve-rollout
```

**Ingest hardening (security review M1/M5):**
- **Clamp `client_ts` server-side** to `[server_now − small_window, server_now + skew]`;
  reject/dead-letter rows outside it, and store the server-clamped `ts`. This stops a leaked
  token from poisoning rollups or evading purge with far-past/far-future rows.
- **Hard caps** on batch row count and HTTP body size; a per-device rows/day cap.
- **Rate-limit** per device and globally, keyed on the **true client IP** (`CF-Connecting-IP`,
  not spoofable `X-Forwarded-For`).
- **Idempotent** via `INSERT OR IGNORE` on `(device_id, ts)`; a post-downtime backlog uploads in
  one call safely.
- Validate/clamp payloads (title length cap; reject malformed rows).

**Dirty-days rollup (improvements #5).** The nightly job must **not** only roll up "yesterday."
An agent offline for 3 days flushes a backlog *today* dated 3 days back — days already rolled up.
Track the set of `(device_id, day)` touched by each ingest and re-run the **idempotent** rollup
for exactly those dirty days, else those minutes silently vanish from `daily_stats`.

---

## 7. Dashboard

Single-page React app, built to static files, embedded via `//go:embed all:web/dist` and served
with a catch-all falling back to `index.html`. No Nginx, no Node in production — the same binary
that ingests logs serves the UI.

**Central UX principle, stated in the UI:** *"Screentime" = monitor-on time,* with an inline
note on *why* it's the primary signal **and its honest caveat** (it's the best available presence
proxy, not tamper-proof — see §0.1). Input-active is always a secondary, lighter layer.

### 7.1 Key views

- **Overview (landing).** KPI row: household screentime today / this week (monitor-on), delta vs
  prior period, devices reporting, devices online now. Stacked bars of screentime per device over
  the range; a compact "today by hour" strip; a Today/7d/30d range switcher driving the page.
- **Per-device drilldown.** Header (name, last-seen, agent version, online/offline pill, clock-skew
  flag if any). Daily timeline with monitor-on as the solid primary series and input-active
  overlaid fainter so you can *see* the gap. Top apps. Day heatmap. Optional per-user split on
  shared PCs.
- **Top applications/windows.** Horizontal bars ranked by monitor-on minutes per foreground exe;
  expand to underlying titles. Toggle household-vs-device.
- **Signal comparison.** monitor-on vs active vs idle side by side — the view that visually shows
  the §0.1 story, subtly flagging divergences (e.g. monitor-off + input-active = the macro
  fingerprint).
- **Timeline + heatmap.** 24h ribbon per day (monitor-on/off segments, thin input-active track
  beneath) — reads like a sleep tracker. Heatmap = days × hours, cell intensity = monitor-on
  minutes (Tailwind divs, not a chart lib).
- **Device list.** Cards/table: name, relative last-seen, online/offline, agent version
  (highlighted if behind latest / rollout gate), today's screentime, OS/host.

Every aggregate is computed **backend-side** into tidy buckets so the SPA only renders small JSON.

### 7.2 Responsive / aesthetic

"Trimmed modern": neutral zinc/stone base, one restrained accent, generous whitespace,
`rounded-xl` cards with hairline borders, `tabular-nums` for figures, Inter/system sans, data-ink
first. Light/dark via shadcn CSS-variable theming (class toggle in `localStorage`, defaulting to
system); charts inherit the same variables. Desktop = collapsible left sidebar + top-bar range
switcher; mobile = bottom tab bar (or `Sheet`), KPI row stacks, grids collapse to one column,
tables become stacked cards, charts stay full-width via `ResponsiveContainer`. Touch targets
≥ 44px, tap tooltips, segmented-control chips over tiny dropdowns.

### 7.3 Install-from-website (self-enrollment)

An **Add a device** page: (1) name the device, click *Generate installer*; (2) backend mints a
**one-time, short-lived, ≥128-bit enrollment token** bound to a pending device slot (stored
hashed); (3) download the agent with the token **in a sidecar `enroll.json` body, never in the
URL/filename** (so it can't leak into Cloudflare/proxy logs) — the binary is identical for
everyone, only the token differs; **link the installer's minisign signature** (§5.3-5) so the
first binary can be verified out-of-band; (4) a numbered 3-step panel (download → run once →
done), noting it's silent and self-updating; (5) a live "waiting for first check-in… ✓ connected"
status that flips when the agent first reports and exchanges its enrollment token for a durable
per-device API key.

### 7.4 Auth — Cloudflare Access

Put the dashboard (the `/` app shell and `/api/dashboard/*`, `/api/devices*`) behind
**Cloudflare Access** with a one-line email policy — this deletes the users table, password
hashing, session cookies, login page, CSRF handling, and login rate-limiting the plan would
otherwise build. `/api/enroll`, `/api/ingest`, and `/agent/*` stay **outside** Access (roaming
agents must reach them; they have their own token/signature auth). Agents (per-device bearer
tokens) and humans (Access) remain two separate credential planes.

### 7.5 Observability — you asked "how do I know an agent stopped?"

- **Offline push, not just a pill.** A backend ticker fires a webhook (ntfy / Pushover / Telegram
  / email) when a device that was reporting regularly goes silent for N minutes. For the
  parental-oversight case, a kid killing the agent is exactly the event to surface. Frame it as
  *"device X stopped reporting"* — the backend can't distinguish "PC off" from "agent killed."
- **Backend dead-man's switch.** The backend pings an external monitor (healthchecks.io or a
  Cloudflare health check) every minute via `/healthz`, so *backend/LXC death* is detected —
  otherwise a dead server means no ingest, no dashboard, **and** no alarm.

---

## 8. Deployment on Proxmox

The backend is a single statically-linked Go binary that embeds the SPA, uses a local SQLite
file, and serves its own agent artifacts. No app server, no DB server, no object store at runtime.

### 8.1 Host: unprivileged Debian 12 LXC + systemd

Docker buys nothing here and costs a daemon; an LXC over a VM because it boots in ~1s and idles at
tens of MB. Create it unprivileged (1 vCPU, 512 MB, 8 GB — oversized), run as a dedicated non-root
`fabscreentime` user. Binaries in `/usr/local/bin`.

```ini
# /etc/systemd/system/fabscreentimed.service
[Unit]
Description=FabScreenTime backend
After=network-online.target
Wants=network-online.target
[Service]
User=fabscreentime
ExecStart=/usr/local/bin/fabscreentimed --config /etc/fabscreentime/config.yaml
Restart=always
RestartSec=2
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/fabscreentime
PrivateTmp=true
LoadCredential=tunnel-token:/etc/fabscreentime/tunnel-token   # secrets via systemd creds, not env
[Install]
WantedBy=multi-user.target
```

Data lives at `/var/lib/fabscreentime/data.db`; everything else is stateless and reproducible.
**Don't auto-update the backend** — it's your server: copy a new binary and `systemctl restart`
(a three-line deploy script). The silent auto-update machinery is for agents only.

### 8.2 Reachability: Cloudflare Tunnel (one overlay only)

`cloudflared` runs inside the LXC and dials *out* to Cloudflare, giving a stable public HTTPS
hostname with **no inbound port and no exposed residential IP** — decisive because agents roam
across arbitrary networks and the owner's phone must reach the dashboard from anywhere. Rejected:
port-forward + DDNS (opens the network, breaks on CGNAT); Tailscale mesh (a client on every
roaming device — defeats the lean agent). Use **Cloudflare Access for admin SSH too** rather than
also running Tailscale — one overlay, not two.

**Privacy trade-off, stated plainly (security review M3):** Cloudflare terminates TLS at the
edge, so it can see window titles (which encode URLs, document names, chat contacts) and tokens in
cleartext. For a household tool with Cloudflare as an accepted trusted party this is usually fine —
but if titles-to-Cloudflare is unacceptable, the alternative is Tailscale-only (accepting the
per-device client cost) or application-level encryption of the `window_title` column. Decide
consciously; don't discover it later.

### 8.3 TLS / proxy

TLS is non-negotiable because agents fetch update code over this channel (the payload is *also*
signed, §5.3). **Default: `cloudflared` → `localhost:8080` directly** — Cloudflare provides the
public cert; no Caddy, no certbot, no extra daemon. (Only add an in-box Caddy hop if you later
want end-to-end origin TLS; it's not needed for a loopback hop.)

> On the agent's TLS pin: the agent's TLS peer is *Cloudflare*, whose cert chains to public CAs,
> so "pin the backend CA" is near-useless as written. **Rely on payload signing (the real
> control) + standard TLS validation**, and drop the transport pin claim rather than imply false
> assurance.

### 8.4 Backups & DR (no extra daemon)

Two layers, both already-owned: **(1)** a one-line nightly cron
`sqlite3 /var/lib/fabscreentime/data.db ".backup /nas/fabscreentime-$(date +%F).db"` to the NAS;
**(2)** Proxmox `vzdump`/PBS nightly snapshot of the whole LXC (keep ~7), capturing config,
binary, and tunnel creds. **DR drill (write it down once):** rebuild LXC → drop the binary →
restore the latest `.backup` → restore tunnel creds → `systemctl enable --now`. ~15 minutes.

Backup-privacy caveats (security review M2/M8):
- Deleting rows at 90 days does **not** remove them from prior snapshots/`.backup` files — those
  hold raw titles indefinitely. Either exclude the raw `samples` table from long-term backup (back
  up only the title-free rollups) **or** document it and match backup retention. Verify at-rest
  encryption on LXC storage **and** the NAS target.
- Scope the NAS backup account to a **write-only, chrooted, ideally append-only** path so a popped
  backend can't wipe or read history.

### 8.5 Build & release (GitHub Actions, one tag-triggered workflow)

One `release.yml` on `v*` tags:
1. **SPA** — `npm ci && npm run build` → static `dist/`.
2. **Backend** — Go with the SPA embedded; `CGO_ENABLED=0` + `modernc.org/sqlite` = clean
   cross-compile.
3. **Agent** — `GOOS=windows GOARCH=amd64`,
   `-ldflags "-s -w -H=windowsgui -X main.Version=${GIT_TAG}"`; then **sign the manifest with the
   offline minisign key** (§5.3). Attach the agent **and its signature** to the GitHub Release
   (the out-of-band first-install source, §5.3-5).
4. **`windows-latest` smoke job** — actually *run* the agent ~10s and assert it emits samples.
5. **`govulncheck`** on the backend deps (since the backend is intentionally *not* auto-updated).

The deploy step drops the signed `agent.exe` + manifest into `/var/lib/fabscreentime/agent/`.
**On Authenticode/SmartScreen:** in-place auto-updates written by the agent carry no
Mark-of-the-Web, so only the first browser download could trip SmartScreen. Start **without** a
separate Authenticode signing infrastructure; if the first-install prompt is annoying, pre-trust a
self-signed publisher cert. **Prefer exclusion-by-publisher/hash over a path exclusion** —
excluding the *user-writable* `%LOCALAPPDATA%\FabScreenTime\` from Defender turns it into a
standing unscanned code-drop location for *any* process (security review M6). Keep CI minimal: no
matrix, no per-commit releases.

---

## 9. Security, Privacy & Consent

**Headline: the silent auto-update channel is the whole ballgame.** Design so that popping the
Proxmox box **leaks data** (bad, recoverable) but does **not own every household PC**
(catastrophic). That firebreak is §5.3 — offline (ideally hardware-backed) signing key, **two**
pinned public keys, a signature that binds `{version, build, hash, mandatory}` as one blob,
monotonic versioning, same-origin download URLs, verify-before-swap, out-of-band first install —
built **first** (Phase 1), before a second machine is enrolled.

**Agent ↔ backend auth.** Per-device high-entropy bearer tokens, stored hashed, one per device
(never a shared fleet secret), DPAPI-encrypted on the client (§4.8). Revocation = flip one row.
**Enrollment tokens are the dangerous credential:** ≥128-bit, one-time, short-lived (minutes),
delivered in the file **body not the URL**, consumed on first check-in, rate-limited on the true
client IP, and logged with source IP + hostname.

**Data minimization — hard rule: never keylog.** Capture only foreground window *title* + *exe*
strings and an idle *flag/ms* — no keystrokes, no clipboard. **Window titles are the sensitive
part** (URLs, document names, contacts, and data about third parties who never consented):
mitigate cheapest-first with truncation/normalization and a **per-device title opt-out**
(`log_titles` → logs exe + activity, drops the title) for adults' machines. Output-encode titles
in the dashboard (a window titled `<script>` is attacker-influenceable text → escape to prevent
stored XSS). Parameterized queries throughout. Remember titles also live in backups (§8.4).

**Consent boundary (plain, no moralizing).** Deploy **only** on machines you own or are
authorized to monitor — household self-monitoring / parental oversight is legitimate. Keep the
enrollment registry as the record of scope. **Household awareness is the right default:**
invisibility means "not distracting," not "secret from the people being measured" — adults in the
house should know the system exists (for minors it's ordinary parental oversight). A small
*discoverable* presence on adult machines (an "about" entry) makes "not secret" real rather than
aspirational. Do **not** install on any machine with managed/corporate EDR (work/school) — it will
be flagged *and* it crosses the consent line. Note the GDPR "purely household activity" exemption
is narrow and generally does **not** cover systematic monitoring of other capable adults; treat
titles as personal data and keep the system inside the household.

**Host hardening.** Non-root service user; systemd sandboxing (already in §8.1); secrets via
`LoadCredential=`, not env; scoped/append-only backup creds (§8.4); `govulncheck` in CI (§8.5).

**AV/EDR reality — be honest.** The agent is behaviorally indistinguishable from spyware. Keep
persistence a **normal, inspectable Scheduled Task** (invisible to a normal user is the
requirement; hidden from the administrator/AV is not). Prefer signing/publisher trust over a
blanket writable-path Defender exclusion.

**Build in this priority order:** (1) signed updates + dual pinned keys + monotonic versioning +
out-of-band first install — the botnet firebreak; (2) per-device tokens + one-time enrollment
tokens + revocation + ts-clamping/batch caps; (3) no keylogging + title truncation/opt-out; (4)
TLS + authenticated rate-limited ingestion; (5) Cloudflare Access on the dashboard + host
hardening; (6) AV hygiene, owner-controlled machines only; (7) enrollment registry + household
awareness + offline alerting. Items 1–3 are the ones that, if skipped, turn this from a reasonable
tool into something genuinely dangerous.

---

## 10. Phased Build Roadmap

Each phase is independently verifiable and keeps the system runnable. Don't enroll a second
machine until Phase 1's update security exists.

### Phase 0 — Walking skeleton + validate the core premise
- **Signal-validation spike (§0.1) — do this before anything else, it is the gating risk.** A
  ~30-line probe on one real household PC logging **all three** candidate signals once a second —
  `QueryDisplayConfig` active-monitor count, `GUID_SESSION_DISPLAY_STATUS`, and
  `GetLastInputInfo`; capture the four traces (normal use / walk-away timeout / **physically power
  the monitor off the way the household does** / real autoclicker running with the monitor off).
  **The decisive check:** does the active-monitor count drop to 0 when the monitor is powered off?
  If yes → connection-presence is the primary signal, proceed. If no (HDMI keeps it connected) →
  work the §0.1 fallback ladder (DDC/CI probe, changed off-method, or accept-and-bucket) **before**
  building Phase 2 on it.
- Go monorepo: `cmd/fabscreentimed`, `cmd/agent`, `internal/shared` (JSON contract). `Sampler`
  interface + Linux stub (§4.1) so the agent builds/tests on CI.
- Backend: `net/http.ServeMux` + `modernc.org/sqlite`, `devices` + `samples`, `POST /api/ingest`
  (`INSERT OR IGNORE`), stub `GET /api/dashboard/summary`. No auth yet.
- Agent: foreground window + exe + idle (skip monitor for now), bounded on-disk queue, POST every
  60s. Build `-H=windowsgui`; run manually.
- **Verify:** rows land in SQLite; summary returns them; the agent unit tests pass on Linux CI; the
  `windows-latest` smoke job produces samples.

### Phase 1 — The security firebreak (before ANY second machine)
- Offline minisign keypair (hardware-backed if possible); **two** public keys pinned. Canonical
  signed manifest binding `{version, monotonic_build, sha256, timestamp, mandatory}`;
  `/agent/manifest` + `/agent/download`.
- Agent self-update via `minio/selfupdate`: piggyback version on `/api/ingest`; verify sig + hash +
  `build > current` **before** the atomic rename-swap; named-mutex guard; relaunch with backoff;
  fail-closed. Same-origin download URL. Out-of-band first-install: attach signed agent to the
  GitHub Release.
- TLS end-to-end (Cloudflare).
- **Verify:** a running `v1` updates itself to `v2` silently; it **rejects** a tampered binary, an
  unsigned/mismatched manifest, a downgrade, and an off-origin URL.

### Phase 2 — Monitor signal + silent autostart (the core insight, made real)
- Message-only window + **message-pump goroutine** (`LockOSThread`). **Primary:** connection
  presence via `QueryDisplayConfig` + `WM_DISPLAYCHANGE` + `RegisterDeviceNotification(GUID_DEVINTERFACE_MONITOR)`
  (§4.4a). **Complementary:** `RegisterPowerSettingNotification(GUID_SESSION_DISPLAY_STATUS)`
  (§4.4b); atomic caches; re-check both on resume. `WTSRegisterSessionNotification` → force
  off/unknown on disconnect/lock; count only the active console session. Store raw
  `monitors_active` + `display_power` + derived `monitor_on` + `idle_ms`, plus `monitor_events`
  transitions (§4.5). Fix idle math to 32-bit unsigned (§4.3).
- Hidden per-user "At log on" Scheduled Task (LIMITED, restart-on-failure, battery-safe);
  self-copy to `%LOCALAPPDATA%`. DPAPI-encrypt the token; clock reconciliation via `server_time`.
- Nightly **dirty-days** rollup → `daily_stats` + `daily_app_stats` (purge deferred).
- **Verify:** monitor off/on flips `monitor_on`; disconnect forces off; reboot restarts the agent
  invisibly (no window/tray/console); rollups match interval integrals; a wrong-clock device
  doesn't corrupt days.

### Phase 3 — Auth, enrollment & tokens
- Per-device hashed bearer tokens; `POST /api/enroll` (≥128-bit one-time token in body) → durable
  per-device token on first check-in; revocation flag; ts-clamping + batch/body/day caps +
  true-IP rate-limiting on ingest.
- Dashboard behind **Cloudflare Access**; `/api/enroll` + `/api/ingest` + `/agent/*` stay public.
- **Verify:** enroll a fresh machine end-to-end; revoke it → uploads 401; a device token can't
  reach dashboard endpoints; the dashboard requires Access.

### Phase 4 — Dashboard MVP
- React + Vite + TS + Tailwind v4 + shadcn/ui, embedded via `go:embed`; TanStack Query +
  react-router. Overview (KPI row + per-device chart + range switcher), Device list, Per-device
  drilldown with the monitor-on-primary / input-active-secondary framing + the honest "why
  monitor-on (and its caveat)" note. Recharts via the shadcn Chart wrapper.
- **Verify:** real aggregated data renders on desktop and mobile, light and dark.

### Phase 5 — Install-from-website + remaining views
- **Add a device** page: name → mint token → per-device installer download (token in body, signed
  agent linked) → live "waiting… ✓ connected" polling.
- Top-apps, signal-comparison, daily timeline ribbon, day heatmap (Tailwind divs). Per-device title
  opt-out surfaced; output-encode all titles.
- **Verify:** a non-technical household member adds a PC in two clicks + one silent run and watches
  it appear online.

### Phase 6 — Production deployment on Proxmox
- Unprivileged Debian 12 LXC; `fabscreentimed` under the hardened systemd unit; `cloudflared` →
  `localhost:8080` (no Caddy). Nightly `sqlite3 .backup` → NAS + Proxmox `vzdump`. Run the DR
  drill once and write it down.
- **Verify:** reach the dashboard from a phone off-network (through Access); restore the DB from a
  `.backup`; a roaming agent reports from an outside network.

### Phase 7 — Availability, observability & release automation
- Owner-gated / canary rollout (§5.4); auto-rollback on crash-loop; complete `--uninstall`.
- Offline-device push alerts + backend dead-man's switch (§7.5); `/healthz`.
- Tag-triggered `release.yml` (SPA → embed → backend; signed Windows agent; `windows-latest`
  smoke; `govulncheck`); deploy script drops artifacts.
- **Verify:** a bad build rolls back automatically and doesn't brick the canary; a killed agent
  raises an alert; a killed backend raises the dead-man alert; `git tag v*` yields a signed agent a
  deployed backend serves.

### Phase 8 — Polish & hardening
- Tamper-evidence (agent-version-per-machine, self-update audit log, behind-latest flags);
  per-user attribution on shared PCs; clock-skew flags; enrollment-registry view.
- Deferred purge + `incremental_vacuum` if/when the DB grows; verify backup at-rest encryption +
  append-only scope.
- UI refinement pass with Claude (trimmed-modern aesthetic, touch targets, simplified mobile
  axes/legends).
- **Verify:** revoke a device → graceful self-uninstall; the dashboard flags a device stuck on an
  old agent or with a skewed clock.

---

## Appendix — the biggest changes the review pass produced

1. **Repointed the core metric from display *power state* to monitor *connection/presence*** and
   added a Phase-0 validation spike — because the user physically powers the monitor off, which
   `GUID_SESSION_DISPLAY_STATUS` does *not* detect. Connection loss (`QueryDisplayConfig`) does,
   but only on DisplayPort (HDMI often keeps the link up), so it must be tested on the household's
   actual hardware (§0.1). *This is the most important change.*
2. **Windows correctness fixes:** a real message-pump goroutine (or the monitor cache never
   updates); 32-bit unsigned idle math; `OpenProcess` fallback; RDP/disconnect/active-console
   handling; self-update mutex + relaunch backoff; `session_minutes` (not "powered").
3. **Security tightening:** sign `{version,build,hash,mandatory}` as one blob; dual pinned keys +
   hardware token; out-of-band verified first install; same-origin download URLs; server-side
   `ts` clamping + batch caps + true-IP rate-limits; enrollment token in body not URL;
   publisher/hash Defender exclusion (not a writable path); backup-retention & Cloudflare-edge
   privacy trade-offs made explicit.
4. **Availability & observability** (previously thin): canary/owner-gated rollout, auto-rollback,
   complete uninstall, offline push alerts, backend dead-man's switch, `windows-latest` CI run,
   `govulncheck`.
5. **Data-integrity:** `idle_ms` column, `monitor_events` for exact minutes, dirty-days rollup,
   clock reconciliation via `server_time`.
6. **Lean simplifications adopted:** dropped Litestream (→ `vzdump` + `.backup` cron), dropped the
   Caddy hop, dropped `chi` (→ stdlib mux), dashboard auth via Cloudflare Access, one overlay
   network, deferred purge/VACUUM.
7. **Two decisions surfaced with recommendations** (§0.2–0.3): keep silent auto-update (with a
   rollout gate) and keep the React dashboard (justified by the "modern, trimmed UI" ask), each
   with its documented cheaper alternative.
