# FabScreenTime

A lean, self-hosted screentime tracker for **self-owned Windows PCs**. An invisible per-PC agent
records active window (title + exe), idle state, and **monitor on/off** once a minute; a single
Go backend on a Proxmox server stores it and serves a responsive dashboard that aggregates across
every enrolled machine. New machines enroll from the website.

> **Status: planning.** No code yet. The full development plan — architecture, tech stack, data
> model, security model, and a phased roadmap — is in **[PLAN.md](./PLAN.md)**.

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
