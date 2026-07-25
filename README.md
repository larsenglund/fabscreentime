# FabScreenTime

A lean, self-hosted screentime tracker for **self-owned Windows PCs**. An invisible per-PC agent
records active window (title + exe), idle state, and **monitor on/off** once a minute; a single
Go backend on a Proxmox server stores it and serves a responsive dashboard that aggregates across
every enrolled machine. New machines enroll from the website.

> The full development plan — architecture, tech stack, data model, security model, and a phased
> roadmap — is in **[PLAN.md](./PLAN.md)**. All roadmap phases (0–8) are implemented, tested, and
> deployed. For current status and how to run/develop/release it on Windows, see
> **[STATUS.md](./STATUS.md)**; for production deploy/DR, see **[deploy/RUNBOOK.md](./deploy/RUNBOOK.md)**.

## The one idea to know

**Monitor-on time is the primary "screentime" metric**, not raw input activity — autoclickers can
forge mouse/keyboard input indistinguishably at the OS API, but they run with the monitor
physically powered off. The catch: a physical power-off is only detectable via monitor
**connection/presence** (`QueryDisplayConfig`), which drops reliably on **DisplayPort** but often
not on **HDMI** — so the metric must be validated on the real hardware first. See
[PLAN.md §0.1](./PLAN.md) for the mechanism, the DP-vs-HDMI dependency, and the fallback ladder.

## Intended shape (see PLAN.md for the why)

- **Agent + backend:** Go — one console-less Windows `.exe`, one static Linux binary, shared JSON
  contract.
- **Storage:** SQLite (WAL, pure-Go `modernc.org/sqlite`).
- **Dashboard:** React + Vite + Tailwind + shadcn/ui, embedded in the backend binary.
- **Host:** unprivileged Debian LXC on Proxmox, exposed via Cloudflare Tunnel; backups via
  `vzdump` + a nightly `sqlite3 .backup`.
- **Auto-update:** silent, with **signed manifests + pinned keys** so a compromised backend can't
  push arbitrary code (this is a hard requirement — see PLAN.md §5).

## Scope & consent

Deploy only on machines you own or are authorized to monitor (household self-monitoring / parental
oversight). Never keylogs — only window titles + activity flags. Not for corporate/EDR-managed
devices. See [PLAN.md §9](./PLAN.md).

---

## Development

Requires Go (see `go.mod` for the version). Pure-Go dependencies only — no cgo, no C toolchain.

### Layout

```
cmd/fabscreentimed   backend: JSON API + dashboard + SQLite, one static binary
cmd/agent            Windows agent (stub sampler on non-Windows so it runs on CI/dev)
cmd/montest          Phase 0 monitor-signal probe (Windows-only) — see PLAN.md §0.1
internal/shared      JSON contract shared by agent and backend
internal/agent       platform-neutral agent core, Sampler interface, bounded queue, uploader
internal/server      HTTP handlers, SQLite store, rollups; serves the embedded dashboard
```

The agent's hard logic sits behind a `Sampler` interface with a real Win32 implementation
(`sampler_windows.go`) and a Linux stub (`sampler_stub.go`), so everything builds and tests on any
OS (PLAN.md §4.1).

### Build, test, run

```bash
go test ./...                      # unit tests (Linux/macOS/Windows)
go run ./cmd/fabscreentimed        # backend on :8080, SQLite at ./fabscreentime.db
go run ./cmd/agent -server http://localhost:8080 -interval 5s   # stub agent → backend
```

Open <http://localhost:8080> for the embedded React dashboard (`web/dist` is committed, so the
backend builds without Node). `scripts/build.sh [version]` produces the Linux backend plus the
silent (`-H=windowsgui`) Windows `agent.exe` and `montest.exe` in `dist/`. Windows-specific
toolchain notes (Go path, Node 22, SSH push) are in [STATUS.md §5](./STATUS.md).

### Phase 0: validate the monitor signal first

The one gating question (PLAN.md §0.1) is whether physically powering a monitor off is detectable.
Build and run the probe on a real Windows PC, then power the monitor off for ~30s and back on:

```
GOOS=windows GOARCH=amd64 go build -o montest.exe ./cmd/montest
montest.exe            # logs monitors_active + idle_ms once/second to montest.csv
```

If `monitors_active` drops to 0 while the monitor is off, connection-presence is a clean signal for
that machine (typical of DisplayPort). If it stays put (typical of HDMI), that machine needs the
fallback ladder in PLAN.md §4.4c. Test at least one DisplayPort and one HDMI PC.

### Signed releases (Phase 1)

Auto-update is fail-closed: the agent applies a new build only if its manifest is signed by a key
pinned in the agent binary, binds a strictly newer build number, and points at a same-origin URL —
all verified before anything is downloaded or swapped (PLAN.md §5.3). A compromised backend cannot
push code.

```bash
go run ./cmd/fst-sign genkey -out release.key      # once; keep release.key OFFLINE
#   → prints a public key; paste it into internal/agent/pinnedkeys.go and rebuild the agent.
go run ./cmd/fst-sign sign -key release.key -in dist/agent.exe -version 1.2.0 -build 2 \
      -out dist/manifest.json
go run ./cmd/fst-sign verify -pub <hex> -manifest dist/manifest.json -bin dist/agent.exe
```

Drop `agent.exe` + `manifest.json` into the backend's `-agentdir`; it serves them at
`/agent/manifest` and `/agent/download` and advertises the update on the next `/api/ingest`.

### Status

**All roadmap phases (0–8) are implemented, tested, and deployed.** The backend runs in production
on the household Alpine VM (LAN-only) and a small fleet of Windows agents enrolls, reports, and
silently self-updates against it. Highlights: signed fail-closed auto-update (tampered / unsigned /
downgrade / off-origin / wrong-hash all rejected before the binary is touched), the validated
DDC/dxva2 monitor-off signal, per-device tokens + enrollment, an embedded React dashboard, one-click
website install, crash-loop auto-rollback, offline/dead-man alerts, and Phase-8 tamper-evidence
(behind-latest + clock-skew flags, self-update audit log).

The current status, deliberate deferrals (notably external reachability / dashboard auth), and the
local run/develop/release guide live in **[STATUS.md](./STATUS.md)**; the roadmap and design
rationale are in [PLAN.md §10](./PLAN.md).
