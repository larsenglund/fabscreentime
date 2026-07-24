# FabScreenTime — deployment & DR runbook

Production host: the household **Alpine VM (Proxmox vmid 101, 192.168.1.6)**.

> **Why not the plan's fresh Debian LXC + systemd (PLAN.md §8.1):** this VM already
> runs the household Docker stack (pihole = LAN DNS, dyndns/SSL, and the previous
> Node "screen time" dashboard), so the backend is deployed as a container here
> instead of a systemd unit. The Go binary is static (`CGO_ENABLED=0`), so it runs
> unchanged on Alpine/musl.

## Access

The SSH key is **not** authorized on this VM — administer it through the Proxmox
API's qemu-guest-agent exec (the same pattern the fabtrade project uses):

```
POST https://192.168.1.8:8006/api2/json/nodes/proxmox/qemu/101/agent/exec
GET  .../agent/exec-status?pid=<pid>
```
with header `Authorization: PVEAPIToken=<token from ~/claude_proxmox_token.txt>`,
using `curl.exe` (PowerShell's `Invoke-RestMethod` mangles the header format).

## Blast-radius rules for this host

1. **Never restart the household compose project** (`/root/docker-compose.yml`).
   Pihole is the LAN's DNS — bouncing it blips every device in the house.
2. **Never start `ib-gateway` / `algo-trader` / `ops-monitor`.** They are stopped
   deliberately (they'd fight the fabtrade stack over the same IB account).
3. FabScreenTime lives in its **own compose project** (`-p fabscreentime`,
   `/opt/fabscreentime/deploy`), so `up`/`down` here cannot touch the above.
4. **Do not reboot the VM** casually — see rule 1.
5. Host port **8090** (8080 belongs to the household stack's ops-monitor).

## Deploy / update

```sh
sh /opt/fabscreentime/deploy/deploy.sh                 # current branch
sh /opt/fabscreentime/deploy/deploy.sh <git-ref>       # a specific ref
```
Idempotent: fast-forwards the checkout, rebuilds the image, recreates only this
container, then waits for `/healthz`.

## Publishing a new **agent** release (auto-update)

The agent release is served from the `fst_agent` volume (`/srv/agent` in the
container). After signing a build locally (`scripts/release-local.ps1` produces
`agentrelease/agent.exe` + `manifest.json`), get the two files onto the box and
`docker cp` them in; the running fleet updates within one ingest cycle (the
backend hot-reloads `manifest.json` on mtime change — no restart):

```sh
docker cp agent.exe      fabscreentimed:/srv/agent/agent.exe
docker cp manifest.json  fabscreentimed:/srv/agent/manifest.json
docker run --rm -v fabscreentime_fst_agent:/a alpine:3.22 chown -R 10001:10001 /a
```

**Getting the files onto the box (no SSH there).** Two options:

1. **HTTP pull** — serve `agentrelease/` from the dev box and `wget` from the VM.
   Watch out: a fresh `go run` file-server is a new binary each time and Windows
   Firewall may block inbound to it (symptom: `wget` times out from the VM).
2. **Proxmox guest-agent file-write** (firewall-independent, the reliable path).
   The API caps `content` at **61440 chars**, so gzip the exe, base64 it, split on
   4-char boundaries into ≤60000-char parts, `file-write` each with `encode=0`
   (PVE base64-decodes and writes raw bytes), then on the guest
   `cat parts/* | gunzip > agent.exe` and verify the sha256 against the manifest
   **before** `docker cp`.

**The served agent must be the ProgramData-aware build** (`InstallDir` →
`%ProgramData%`). An older agent that installs to `%LOCALAPPDATA%` will be
silently blocked by Windows from auto-starting (see the agent notes) — so every
website "Add device" install would fail. Re-stage after any agent change.

## Backups (two layers)

1. **Nightly `sqlite3 .backup`** (file-level, WAL-safe) — cron at 03:17:
   ```sh
   sh /opt/fabscreentime/deploy/backup.sh /root/backups/fabscreentime 14
   ```
   Writes `fabscreentime-YYYY-MM-DD.db.gz`, keeps 14 days.
2. **Proxmox `vzdump`/PBS snapshot** of VM 101 (machine-level) — covers the
   container definition, volumes, and host config.

> Privacy note (PLAN.md §8.4): backups contain raw `samples`, including window
> titles for devices that have **not** opted out. Match backup retention to how
> long you're willing to keep titles, and keep the backup target access-scoped.

## DR drill — restore the database

```sh
sh /opt/fabscreentime/deploy/restore.sh \
   /root/backups/fabscreentime/fabscreentime-YYYY-MM-DD.db.gz
```

Stops the backend, swaps the snapshot in, clears the stale WAL/SHM, fixes
ownership, restarts, and prints the restored row counts.

**Drill performed 2026-07-24 and passed** (19 live rows → 11 = the snapshot,
device registry intact, healthy). It caught two failure modes now handled by the
script — worth knowing if you ever restore by hand:

- `docker cp` writes the file as **root**, but the container runs as **uid
  10001**, so a hand-copied DB crash-loops the backend with
  `open store: unable to open database file (14)`. Fix:
  `docker run --rm -v fabscreentime_fst_data:/d alpine:3.22 chown 10001:10001 /d/data.db`
- The old `data.db-wal` / `data.db-shm` must be deleted with the swap, or SQLite
  replays the stale WAL over the restored snapshot.

## Known host issues

- **No NTP daemon runs on this VM** (`ntpd` stopped; chrony/openntpd not
  installed), so its clock drifts — an enrolled agent measured the VM **5s
  behind** on 2026-07-24. Harmless today: the agent reconciles against
  `server_time` and the backend clamps `client_ts` (PLAN.md §4.8/§6.3), and 5s is
  far below any flagging threshold. But drift grows, and every metric here is
  timestamp-based, so enabling time sync is worthwhile:
  `rc-service ntpd start && rc-update add ntpd` (busybox ntpd is already present).
  Left undone deliberately — it changes shared household infrastructure, not just
  this app.

## Full-host rebuild

1. Restore VM 101 from the Proxmox/PBS snapshot **or** provision a new Alpine VM
   with Docker.
2. `git clone -b <branch> https://github.com/larsenglund/fabscreentime.git /opt/fabscreentime`
3. `sh /opt/fabscreentime/deploy/deploy.sh`
4. Restore the DB (above), re-add the agent release, re-install the cron entry.
5. Re-point external access (see below).

## External reachability — NOT yet configured

PLAN.md §8.2 specifies a **Cloudflare Tunnel** so roaming agents and the owner's
phone can reach the backend without opening ports, with **Cloudflare Access** on
the dashboard. That step needs the owner's Cloudflare account (an interactive
browser login) and a decision about the hostname, so it is **deliberately left
undone** — today the backend is reachable **on the LAN only**
(`http://192.168.1.6:8090`).

Note this host already runs an `ssl-and-dyndns` container with certbot/Let's
Encrypt for a household dynamic-DNS name; publishing via that path instead of
Cloudflare is a viable alternative, but it means opening an inbound port and it
provides **no equivalent of Cloudflare Access**, so the dashboard would need its
own authentication first. Decide before exposing anything.
