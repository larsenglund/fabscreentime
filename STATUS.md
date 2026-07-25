# FabScreenTime — Project Status & Local (Windows) Guide

The single source of truth for **where the project stands** and **how to run, develop, and
release it on real Windows hardware**. (This file was `HANDOFF.md` while the cloud→local handover
was in flight; the handover is long done, so it's now the living status doc.) Committed on branch
`claude/screentime-app-plan-nttogj`.

> **New here?** Read [`PLAN.md`](./PLAN.md) — the full design, especially **§0** (the key
> decisions and the monitor-signal insight the whole metric hinges on). This file tracks what's
> built, what's deployed, and what's deliberately left undone.

---

## 1. Status at a glance

**All ten roadmap phases (0–8, plus the security firebreak) are implemented, tested, and deployed.**
The backend runs in production on the household Alpine VM; a small fleet of Windows agents enrolls,
reports, and silently self-updates against it.

| Phase | What | State |
|------|------|-------|
| 0 | Walking skeleton + monitor-signal spike | ✅ done; signal validated (see §4) |
| 1 | Signed auto-update firebreak | ✅ done; key pinned, verified in prod |
| 2 | Monitor signal + silent autostart | ✅ done (DDC/dxva2 signal) |
| 3 | Auth, enrollment & per-device tokens | ✅ done |
| 4 | Dashboard MVP (React, embedded) | ✅ done |
| 5 | Install-from-website + remaining views | ✅ done (one-click `.bat`) |
| 6 | Production deployment (Proxmox) | ✅ done (Alpine VM, DR drill passed) |
| 7 | Availability, observability & release automation | ✅ done |
| 8 | Polish & hardening | ✅ done (see §3 for what's in vs. deferred) |

- **Branch:** `claude/screentime-app-plan-nttogj` — **not** merged to a default branch; no PR yet.
- **Production backend:** `http://192.168.1.6:8090` (household Alpine VM, Proxmox vmid 101),
  **LAN-only** — see the deferral in §3. Full deploy/DR details in
  [deploy/RUNBOOK.md](./deploy/RUNBOOK.md).
- **Tests:** `go test ./...` green on Linux + Windows; CI runs the real Win32 code on
  `windows-latest`. The web app typechecks (`tsc --noEmit`) and builds (`vite build`).

### What each later phase delivered

- **Phase 6 — production:** the backend runs as its **own** Docker compose project (`-p
  fabscreentime`) on **port 8090**, isolated from the household stack (pihole/DNS etc.). Nightly
  `sqlite3 .backup` + rotation via cron; a **DR restore drill was performed and passed**. Deployed
  as a container rather than the plan's Debian-LXC-and-systemd because that host already runs the
  household Docker stack; the static `CGO_ENABLED=0` binary runs fine on Alpine/musl.
- **Phase 7 — availability:** crash-loop **auto-rollback** with build quarantine (a bad update that
  restarts repeatedly before ever checking in restores the previous binary and refuses to re-apply
  the bad build); offline-device **push alerts** + a backend **dead-man's switch** (both off by
  default, wired through compose env); `/healthz`; tag-triggered `release.yml` (SPA → embed →
  backend, signed Windows agent, `windows-latest` smoke, `govulncheck`); complete `-uninstall`.
- **Phase 8 — polish & hardening:** the **behind-latest flag** (`/api/devices` reports the latest
  published build; the dashboard badges any device running behind it), the **clock-skew flag** (the
  agent stamps `client_now` on ingest; the server records the agent-minus-server offset and the
  dashboard flags a device whose clock is off by >2 min), and the **self-update audit log**
  (`device_events`: every agent build transition is recorded, a *backwards* move flagged as a
  `downgrade` — a tamper signal — and shown in the device page's "Update history"). The device list
  doubles as the **enrollment registry** (every pending / active / expired / revoked slot).

---

## 2. The one idea (why monitor-on is the metric)

Autoclickers forge mouse/keyboard input indistinguishably at the OS API, but they run with the
monitor **physically powered off**. So the trustworthy "real human screentime" signal is
*monitor on/off*, not input activity. The subtlety (PLAN.md §0.1): a physical power-off is only
visible to Windows on some connections. On this fleet the connected-monitor **count never drops**
on power-off (neither DP nor HDMI here), so the load-bearing signal is the **DDC/CI + dxva2**
probe — see [MONTEST-RESULTS.md](./MONTEST-RESULTS.md) and §4.

---

## 3. Deliberately deferred (and why)

None of these block the working system; each is a conscious "not now", not an oversight.

1. **External reachability (Cloudflare Tunnel + Access), PLAN.md §8.2.** The backend is **LAN-only**;
   roaming agents and off-network phones can't reach it, and the dashboard has **no auth of its
   own**. Enabling the tunnel needs your Cloudflare account (an interactive browser login) and a
   hostname decision. **Do not expose port 8090 as-is.** Options (Cloudflare vs. the host's existing
   `ssl-and-dyndns`) are written up in the runbook — but dashboard auth must come first.
2. **Revoke → agent self-uninstall (Phase 8 "verify" line).** Left out **on purpose**: if an agent
   treated an auth failure as "revoked → delete myself", a transient misconfig or a hostile 401
   could brick or wipe the whole fleet (PLAN.md §5.3 M4). Revocation is enforced **server-side**
   (a revoked token's ingest is 401 and its data stops); decommissioning a machine is the operator
   running `agent.exe -uninstall` (or deleting the device, which also drops its stored data).
3. **Per-user attribution on shared PCs (Phase 8).** The agent records the foreground app, not which
   Windows user was logged in. Adding a per-sample user column + per-user aggregation is a real
   feature, but the household's machines are effectively single-user, so it's not worth the schema +
   UI surface yet.
4. **Deferred purge + `incremental_vacuum` (Phase 8).** The DB is tiny (raw samples for a handful of
   machines). Ship a retention/purge job **if/when** it grows — the schema and rollups already make
   old raw rows redundant once summarized.
5. **Backup at-rest encryption (Phase 8).** Backups contain raw samples incl. window titles for
   devices that haven't opted out. Today they rely on host/target access scoping (runbook privacy
   note); add gpg to `backup.sh` if backups ever leave the trusted host.
6. **Second (rotation) signing key.** Only the primary Ed25519 key is pinned. Generate a second
   offline key and add it to `pinnedUpdateKeysHex` so a primary-key compromise is recoverable
   without re-imaging every agent.
7. **DPMS message-pump watcher (`GUID_SESSION_DISPLAY_STATUS`).** `display_power` stays "unknown";
   `monitor_on` is driven by the validated DDC/dxva2 signal, so this is complementary, not required.
8. **NTP on the Alpine VM.** Measured ~5 s behind; harmless (agent reconciles via `server_time`, the
   server clamps timestamps) and it's shared household infra, so left alone. See the runbook.

---

## 4. The monitor-signal validation (done)

The one experiment the metric design hinged on (PLAN.md §0.1) is **complete** — results in
[MONTEST-RESULTS.md](./MONTEST-RESULTS.md). On the test machine (GTX 1060; Philips signage over DP,
Dell U2515H over DP **and** HDMI) the topology/connection count never drops on a physical power-off,
so connection-presence is **not** usable here. The working signal is the probe's dxva2 `ddc_power`
column: over DP the physical-monitor handle disappears when the screen is powered off; over HDMI the
VCP `0xD6` reply flips to "off". **That rule is implemented in the agent** (`ddc_windows.go` watcher
→ raw `ddc_power` per sample → `ddcSaysOff` derivation) and verified end-to-end: a physical
power-off flips `monitor_on`, emits the `monitor_events` pair, and lands in the daily rollup.

To re-run the probe on a new machine/GPU:

```powershell
go build -o montest.exe .\cmd\montest
.\montest.exe        # power the monitor off ~30s, back on, Ctrl+C; inspect montest.csv
```

Every enrolled machine is calibrated by the same probe logic at runtime, so a new GPU is
self-correcting; a manual `montest` round is only for curiosity or a surprising panel.

---

## 5. Local Windows toolchain (this machine)

- **Go** is at `C:\Program Files\Go\bin\go.exe` and is **not on PATH** in scripted shells — call it
  (and `gofmt.exe`) by full path.
- **Node 22** lives at `%LOCALAPPDATA%\Programs\nodejs22` (system PATH still resolves an older Node
  first). Prepend it before any `npm`/`npx` so the spawned Vite uses v22. `scripts/build-ui.ps1` and
  `scripts/release-local.ps1` handle this; `web/dist` is **committed and embedded**, so building the
  **backend** never needs Node.
- **`git push`** goes over the repo's SSH deploy key: origin is
  `git@github-fabscreentime:larsenglund/fabscreentime.git` (alias in `~/.ssh/config`). **Never**
  switch the remote back to HTTPS — there's no github.com credential on this box.
- `core.autocrlf=true`: a bare `gofmt -l .` flags every file (CRLF vs. the repo's LF). That's
  line-ending noise — run `gofmt -w` only on files you changed; Linux CI is the source of truth.

---

## 6. Run the whole thing locally (backend + agent on one PC)

**Backend** (runs on Windows; production is Linux):

```powershell
& "C:\Program Files\Go\bin\go.exe" run .\cmd\fabscreentimed -db .\dev.db -agentdir agentrelease
# open http://localhost:8080
```

**Agent** (console build so you can watch logs; the *silent* build is §8):

```powershell
& "C:\Program Files\Go\bin\go.exe" run .\cmd\agent -server http://localhost:8080 -interval 5s -datadir .\agentdata
```

Enroll from the dashboard's **Add device** (mints a one-time secret + a double-clickable installer),
or manually with `-enroll <secret-from-/api/enroll/prepare>`. After first enroll the DPAPI-encrypted
token is saved in `<datadir>\credentials.json`; drop `-enroll`.

---

## 7. Ship a new agent build (auto-update)

Running agents self-update within one ingest cycle once a signed release is served. Locally:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\release-local.ps1 -Version 0.7.0
# → builds the silent agent.exe, signs it (release.key, OFFLINE), writes agentrelease\{agent.exe,manifest.json}
```

Then get those two files onto the production box and `docker cp` them into `fabscreentimed:/srv/agent`
(the backend hot-reloads `manifest.json` on mtime — no restart). The transfer is SSH-less; see
[deploy/RUNBOOK.md](./deploy/RUNBOOK.md) → "Publishing a new agent release". **The served agent must
be the ProgramData-aware build** or website installs won't auto-start (see §8).

To prove the firebreak locally, re-sign with a lower build, tamper `manifest.json`, or point `url`
off-origin — the agent logs a rejection and keeps running the old build. These paths are covered by
`go test ./internal/agent -run SelfUpdate`.

---

## 8. Silent operation + autostart (Phase 2)

**Silent build** (GUI subsystem — no console window):

```powershell
& "C:\Program Files\Go\bin\go.exe" build -ldflags "-s -w -H=windowsgui -X main.Version=0.7.0 -X main.Build=7" -o dist\agent.exe .\cmd\agent
```

**Install hidden autostart** (copies to `%ProgramData%\FabScreenTime\agent.exe`, registers a hidden
"at logon" Scheduled Task, starts it):

```powershell
dist\agent.exe -install -server http://192.168.1.6:8090
```

Verify: `Get-ScheduledTask -TaskName FabScreenTimeAgent` reaches **Running**; Task Manager shows
`agent.exe` with no window/tray; the dashboard shows the device. Logs:
`Get-Content "$env:ProgramData\FabScreenTime\agent.log"`.

**Install location is `%ProgramData%`, not `%LOCALAPPDATA%`** (learned the hard way): a hidden Task
launching an unsigned exe from `AppData\Local` is silently blocked by Windows app-reputation
heuristics — the process is never created and *no event is logged* (`schtasks` result `0x80070002`
on an exe that plainly exists), while the same exe runs fine from `%ProgramData%`.

**Uninstall:** `dist\agent.exe -uninstall` (removes the task, schedules exe/dir + data cleanup).

### Antivirus / SmartScreen reality (expected)

A hidden, self-persisting, self-updating exe **looks like spyware to Defender/SmartScreen** — that's
inherent (PLAN.md §9), and the ProgramData location is what makes the hidden autostart launch. Real
deployment wants code-signing / Trusted-Publisher + a scoped exclusion. **Never** disable Defender
globally, and don't install this on any work/school/EDR-managed machine.

---

## 9. Repo layout

```
PLAN.md                     the full development plan (read §0 first)
STATUS.md                   this file
MONTEST-RESULTS.md          the monitor-signal validation results
README.md                   short project intro
cmd/fabscreentimed/         backend: API + embedded dashboard + SQLite, one binary
cmd/agent/                  Windows agent (stub sampler off-Windows; -once/-install/-uninstall)
cmd/montest/                monitor-signal probe (Windows-only)
cmd/fst-sign/               offline signing CLI: genkey / sign / verify
internal/shared/            JSON contract shared by agent & backend
internal/agent/             agent core, Sampler, queue, self-update, rollback, autostart
internal/server/            HTTP handlers, SQLite store, rollups, alerts
internal/update/            Ed25519 manifest sign/verify (the auto-update firebreak)
web/                        React dashboard (Vite/Tailwind); web/dist is committed + embedded
deploy/                     Dockerfile, compose, deploy/backup/restore scripts, RUNBOOK
scripts/                    build-ui.ps1, release-local.ps1, build.sh
.github/workflows/          ci.yml (Linux + windows-latest) and release.yml (tag-triggered)
```

---

## 10. Gotchas & notes

- **SQLite** uses `SetMaxOpenConns(1)` (single writer) — simple and plenty at this scale; don't
  "optimize" it into lock errors.
- **Day boundaries are UTC** for rollups. A configurable household timezone is a later refinement
  (PLAN.md §6.2) — note it when reading daily numbers near midnight.
- **Backend on Windows vs. Linux:** identical code. Run locally on Windows; deploy the container to
  Proxmox. `.db` files are cross-platform.
- **CI** builds/tests on Linux and runs the real Win32 code on `windows-latest`; keep it green
  (`gofmt`, `go vet`, `go test`).
- **Self-update mutex handoff:** the child process acquires the single-instance mutex with a short
  retry, so a relaunch during update doesn't race the exiting parent into "another instance is
  running; exiting" and leave *no* agent running. Don't remove the retry loop in `cmd/agent/main.go`.

---

## 11. Git workflow

- Keep developing on **`claude/screentime-app-plan-nttogj`**.
- `go test ./...` + `gofmt -w` (changed files) before committing; CI enforces format/vet/test.
- Push over the SSH remote (§5). No PR yet — open one only when you decide to merge.
