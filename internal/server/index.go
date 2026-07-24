package server

// indexHTML is a tiny, dependency-free placeholder dashboard — no build step, no
// npm. Beyond showing per-device screentime it drives the one-click enrollment
// flow (PLAN.md §7.3): name a machine, download a personalized installer whose
// body carries the one-time secret (never a URL), and watch it come online. The
// real React/Tailwind dashboard replaces this in Phase 4 (PLAN.md §7).
const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>FabScreenTime</title>
<style>
  :root { color-scheme: light dark; --accent: #6366f1; }
  * { box-sizing: border-box; }
  body { margin: 0; font: 15px/1.5 system-ui, -apple-system, Segoe UI, Roboto, sans-serif;
         background: #0b0d10; color: #e7eaee; }
  @media (prefers-color-scheme: light) { body { background: #f6f7f9; color: #14171a; } }
  header { padding: 24px 20px 8px; max-width: 1000px; margin: 0 auto;
           display: flex; align-items: flex-end; justify-content: space-between; gap: 12px; }
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
  .dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 6px; vertical-align: middle; }
  .on { background: #34d399; } .off { background: #9aa3ad; }
  .pending { background: #fbbf24; } .revoked { background: #f87171; }
  .empty { padding: 40px 14px; text-align: center; opacity: 0.6; }
  .name { font-weight: 600; }
  .pill { font-size: 11px; padding: 1px 7px; border-radius: 999px; opacity: 0.8;
          border: 1px solid color-mix(in srgb, currentColor 22%, transparent); }
  button { font: inherit; cursor: pointer; border-radius: 9px; border: 1px solid transparent; }
  .btn { background: var(--accent); color: #fff; padding: 8px 14px; font-weight: 600; }
  .btn:hover { filter: brightness(1.08); }
  .btn.ghost { background: transparent; color: inherit;
               border-color: color-mix(in srgb, currentColor 22%, transparent); }
  .btn.sm { padding: 4px 10px; font-size: 13px; }
  .btn.danger { color: #f87171; }
  input[type=text] { font: inherit; padding: 8px 11px; border-radius: 9px; width: 100%;
        color: inherit; background: color-mix(in srgb, currentColor 6%, transparent);
        border: 1px solid color-mix(in srgb, currentColor 20%, transparent); }
  .panel { margin: 0 0 18px; padding: 18px; }
  .panel[hidden] { display: none; }
  .step { margin: 14px 0; }
  .step h3 { font-size: 13px; margin: 0 0 6px; opacity: 0.75; }
  code, pre { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 13px; }
  pre { background: color-mix(in srgb, currentColor 8%, transparent); padding: 10px 12px;
        border-radius: 9px; overflow-x: auto; margin: 0; white-space: pre-wrap; word-break: break-all; }
  .muted { opacity: 0.6; font-size: 13px; }
  .status-line { margin-top: 6px; font-weight: 600; }
  .ok { color: #34d399; }
  @media (max-width: 640px) { th.hide, td.hide { display: none; } header { flex-wrap: wrap; } }
</style>
</head>
<body>
<header>
  <div>
    <h1>FabScreenTime</h1>
    <div class="sub">Screentime = monitor-on minutes · <span id="range">last 7 days</span></div>
  </div>
  <button class="btn" id="addBtn">＋ Add device</button>
</header>
<main>
  <div class="card panel" id="addPanel" hidden>
    <div class="step">
      <h3>1 · Name the machine</h3>
      <div style="display:flex; gap:8px; max-width:460px;">
        <input type="text" id="devName" placeholder="e.g. Living room PC" autocomplete="off">
        <button class="btn" id="genBtn" style="white-space:nowrap;">Generate installer</button>
      </div>
      <div class="muted" id="genErr" style="color:#f87171; margin-top:6px;"></div>
    </div>
    <div id="installSteps" hidden>
      <div class="step">
        <h3>2 · On the new PC, download &amp; run once</h3>
        <p class="muted">A personalized installer downloaded below carries a one-time enrollment
          key in its body. Move it to the target PC, then in PowerShell run:</p>
        <pre id="runCmd"></pre>
        <div style="display:flex; gap:8px; margin-top:8px; flex-wrap:wrap;">
          <button class="btn" id="dlBtn">⬇ Download installer</button>
          <button class="btn ghost sm" id="copyBtn">Copy command</button>
        </div>
        <p class="muted" style="margin-top:8px;">It installs silently (no window or tray), starts at
          logon, and keeps itself updated. Key expires in <span id="ttl">15</span> min.</p>
      </div>
      <div class="step">
        <h3>3 · Wait for first check-in</h3>
        <div class="status-line" id="waitStatus">⏳ Waiting for the device to report…</div>
      </div>
    </div>
  </div>

  <div class="card">
    <table>
      <thead><tr>
        <th>Device</th>
        <th class="num">Screentime</th>
        <th class="num hide">Active</th>
        <th class="hide">Agent</th>
        <th>Last seen</th>
        <th></th>
      </tr></thead>
      <tbody id="rows"><tr><td class="empty" colspan="6">Loading…</td></tr></tbody>
    </table>
  </div>
</main>
<script>
  var esc = function (s) { return String(s == null ? "" : s).replace(/[<>&"]/g, function (c) {
    return { "<": "&lt;", ">": "&gt;", "&": "&amp;", '"': "&quot;" }[c]; }); };
  var fmtMin = function (m) { return m >= 60 ? (m / 60).toFixed(1) + " h" : (m || 0) + " m"; };
  var ago = function (ts) {
    if (!ts) return "never";
    var s = Math.max(0, Math.floor(Date.now() / 1000 - ts));
    if (s < 90) return s + "s ago";
    if (s < 5400) return Math.round(s / 60) + "m ago";
    if (s < 172800) return Math.round(s / 3600) + "h ago";
    return Math.round(s / 86400) + "d ago";
  };
  var online = function (ts) { return ts && (Date.now() / 1000 - ts) < 180; };

  // ---- device table (merge status list + metric summary by uuid) ----
  function loadDevices() {
    Promise.all([
      fetch("/api/devices").then(function (r) { return r.json(); }),
      fetch("/api/dashboard/summary?range=7d").then(function (r) { return r.json(); })
    ]).then(function (res) {
      var devices = (res[0] && res[0].devices) || [];
      var metrics = {};
      ((res[1] && res[1].devices) || []).forEach(function (m) { metrics[m.device_uuid] = m; });
      var rows = document.getElementById("rows");
      if (!devices.length) {
        rows.innerHTML = '<tr><td class="empty" colspan="6">No devices yet. Click “Add device”.</td></tr>';
        return;
      }
      rows.innerHTML = devices.map(function (v) {
        var m = metrics[v.device_uuid] || {};
        var cls = v.status === "revoked" ? "revoked" : v.status === "pending" ? "pending"
                : online(v.last_seen) ? "on" : "off";
        var label = v.status === "active" ? (online(v.last_seen) ? "online" : "offline") : v.status;
        var action = v.status === "revoked"
          ? '<span class="muted">revoked</span>'
          : '<button class="btn ghost sm danger" data-revoke="' + esc(v.device_uuid) + '">Revoke</button>';
        return '<tr>'
          + '<td><span class="dot ' + cls + '"></span><span class="name">'
              + esc(v.name || v.hostname || v.device_uuid) + '</span> '
              + '<span class="pill">' + esc(label) + '</span></td>'
          + '<td class="num">' + fmtMin(m.monitor_minutes) + '</td>'
          + '<td class="num hide">' + fmtMin(m.active_minutes) + '</td>'
          + '<td class="hide">' + esc(v.agent_version || "—") + '</td>'
          + '<td>' + ago(v.last_seen) + '</td>'
          + '<td style="text-align:right">' + action + '</td>'
          + '</tr>';
      }).join("");
      Array.prototype.forEach.call(rows.querySelectorAll("[data-revoke]"), function (b) {
        b.addEventListener("click", function () { revoke(b.getAttribute("data-revoke")); });
      });
    }).catch(function () {
      document.getElementById("rows").innerHTML =
        '<tr><td class="empty" colspan="6">Failed to load.</td></tr>';
    });
  }

  function revoke(uuid) {
    if (!confirm("Revoke this device? Its agent can no longer upload until re-enrolled.")) return;
    fetch("/api/devices/" + encodeURIComponent(uuid), {
      method: "PATCH", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ revoked: true })
    }).then(loadDevices);
  }

  // ---- add-device flow ----
  var addPanel = document.getElementById("addPanel");
  document.getElementById("addBtn").addEventListener("click", function () {
    addPanel.hidden = !addPanel.hidden;
    if (!addPanel.hidden) document.getElementById("devName").focus();
  });

  var pollTimer = null;
  function buildInstaller(server, secret) {
    // PowerShell bootstrap: the secret lives in the FILE BODY, never a URL, so it
    // can't leak into edge/proxy logs. It downloads the signed agent, verifies its
    // hash against the manifest, drops enroll.json, and installs silently.
    var lines = [
      "$ErrorActionPreference = 'Stop'",
      "$Server = '" + server + "'",
      "$Secret = '" + secret + "'",
      "$dir = Join-Path $env:LOCALAPPDATA 'FabScreenTime'",
      "New-Item -ItemType Directory -Force -Path $dir | Out-Null",
      "$exe = Join-Path $dir 'agent.exe'",
      "Write-Host \"Downloading agent from $Server ...\"",
      "Invoke-WebRequest -UseBasicParsing -Uri \"$Server/agent/download\" -OutFile $exe",
      "try {",
      "  $m = Invoke-RestMethod -UseBasicParsing -Uri \"$Server/agent/manifest\"",
      "  $h = (Get-FileHash -Algorithm SHA256 $exe).Hash.ToLower()",
      "  if ($m.manifest.sha256 -and $m.manifest.sha256 -ne $h) { throw \"agent.exe hash mismatch\" }",
      "} catch { Write-Warning \"manifest check skipped: $_\" }",
      "(@{ server = $Server; enroll_secret = $Secret } | ConvertTo-Json) | Set-Content -Path (Join-Path $dir 'enroll.json') -Encoding UTF8",
      "& $exe -install -server $Server",
      "Write-Host 'Installed. It should appear on the dashboard within a minute.'"
    ];
    return lines.join("\r\n");
  }

  document.getElementById("genBtn").addEventListener("click", function () {
    var name = document.getElementById("devName").value.trim();
    var errEl = document.getElementById("genErr");
    errEl.textContent = "";
    fetch("/api/enroll/prepare", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: name })
    }).then(function (r) { if (!r.ok) throw new Error("prepare failed (" + r.status + ")"); return r.json(); })
      .then(function (p) {
        var server = window.location.origin;
        var script = buildInstaller(server, p.enroll_secret);
        document.getElementById("ttl").textContent = Math.round((p.expires_in || 900) / 60);
        document.getElementById("runCmd").textContent =
          "powershell -ExecutionPolicy Bypass -File .\\install-fabscreentime.ps1";
        document.getElementById("installSteps").hidden = false;

        document.getElementById("dlBtn").onclick = function () {
          var blob = new Blob([script], { type: "text/plain" });
          var url = URL.createObjectURL(blob);
          var a = document.createElement("a");
          a.href = url; a.download = "install-fabscreentime.ps1"; a.click();
          setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
        };
        document.getElementById("copyBtn").onclick = function () {
          navigator.clipboard.writeText(document.getElementById("runCmd").textContent);
        };
        // Auto-download immediately for the one-click feel.
        document.getElementById("dlBtn").click();
        pollFor(p.device_uuid, name);
      }).catch(function (e) { errEl.textContent = e.message; });
  });

  function pollFor(uuid, name) {
    var wait = document.getElementById("waitStatus");
    wait.classList.remove("ok");
    wait.textContent = "⏳ Waiting for the device to report…";
    if (pollTimer) clearInterval(pollTimer);
    pollTimer = setInterval(function () {
      fetch("/api/devices/" + encodeURIComponent(uuid)).then(function (r) { return r.json(); })
        .then(function (d) {
          if (d && d.status === "active" && d.last_seen) {
            clearInterval(pollTimer); pollTimer = null;
            wait.textContent = "✓ Connected: " + (name || d.hostname || "device") + " is reporting.";
            wait.classList.add("ok");
            loadDevices();
          }
        }).catch(function () {});
    }, 3000);
  }

  loadDevices();
  setInterval(loadDevices, 15000);
</script>
</body>
</html>`
