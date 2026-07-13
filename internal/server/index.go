package server

// indexHTML is a tiny, dependency-free placeholder dashboard for Phase 0 — no
// build step, no npm. It fetches /api/dashboard/summary and renders a device
// table so the walking skeleton is visible end to end. The real React/Tailwind
// dashboard replaces this in Phase 4 (PLAN.md §7).
const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>FabScreenTime</title>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body { margin: 0; font: 15px/1.5 system-ui, -apple-system, Segoe UI, Roboto, sans-serif;
         background: #0b0d10; color: #e7eaee; }
  @media (prefers-color-scheme: light) { body { background: #f6f7f9; color: #14171a; } }
  header { padding: 24px 20px 8px; max-width: 1000px; margin: 0 auto; }
  h1 { font-size: 20px; margin: 0 0 2px; letter-spacing: -0.01em; }
  .sub { opacity: 0.6; font-size: 13px; }
  main { max-width: 1000px; margin: 0 auto; padding: 12px 20px 48px; }
  .card { background: color-mix(in srgb, currentColor 4%, transparent);
          border: 1px solid color-mix(in srgb, currentColor 12%, transparent);
          border-radius: 14px; overflow: hidden; }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: 12px 14px; white-space: nowrap; }
  th { font-size: 12px; text-transform: uppercase; letter-spacing: 0.04em; opacity: 0.55; }
  tbody tr { border-top: 1px solid color-mix(in srgb, currentColor 10%, transparent); }
  td.num { font-variant-numeric: tabular-nums; }
  .dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 6px; }
  .on { background: #34d399; } .off { background: #9aa3ad; }
  .empty { padding: 40px 14px; text-align: center; opacity: 0.6; }
  .name { font-weight: 600; }
  @media (max-width: 640px) { th.hide, td.hide { display: none; } }
</style>
</head>
<body>
<header>
  <h1>FabScreenTime</h1>
  <div class="sub">Screentime = monitor-on minutes · <span id="range">last 7 days</span></div>
</header>
<main>
  <div class="card">
    <table>
      <thead><tr>
        <th>Device</th>
        <th class="num">Screentime</th>
        <th class="num hide">Active</th>
        <th class="num hide">Samples</th>
        <th class="hide">Agent</th>
        <th>Last seen</th>
      </tr></thead>
      <tbody id="rows"><tr><td class="empty" colspan="6">Loading…</td></tr></tbody>
    </table>
  </div>
</main>
<script>
  const fmtMin = m => m >= 60 ? (m/60).toFixed(1) + " h" : m + " m";
  const ago = ts => {
    if (!ts) return "never";
    const s = Math.max(0, Math.floor(Date.now()/1000 - ts));
    if (s < 90) return s + "s ago";
    if (s < 5400) return Math.round(s/60) + "m ago";
    if (s < 172800) return Math.round(s/3600) + "h ago";
    return Math.round(s/86400) + "d ago";
  };
  const online = ts => (Date.now()/1000 - ts) < 180;
  fetch("/api/dashboard/summary?range=7d").then(r => r.json()).then(d => {
    const rows = document.getElementById("rows");
    if (!d.devices || !d.devices.length) {
      rows.innerHTML = '<tr><td class="empty" colspan="6">No devices yet. Enroll one to start logging.</td></tr>';
      return;
    }
    rows.innerHTML = d.devices.map(v => ` + "`" + `
      <tr>
        <td><span class="dot ${online(v.last_seen) ? "on" : "off"}"></span>
            <span class="name">${(v.name || v.hostname || v.device_uuid).replace(/[<>&]/g, "")}</span></td>
        <td class="num">${fmtMin(v.monitor_minutes)}</td>
        <td class="num hide">${fmtMin(v.active_minutes)}</td>
        <td class="num hide">${v.sample_count}</td>
        <td class="hide">${(v.agent_version || "—").replace(/[<>&]/g, "")}</td>
        <td>${ago(v.last_seen)}</td>
      </tr>` + "`" + `).join("");
  }).catch(() => {
    document.getElementById("rows").innerHTML =
      '<tr><td class="empty" colspan="6">Failed to load.</td></tr>';
  });
</script>
</body>
</html>`
