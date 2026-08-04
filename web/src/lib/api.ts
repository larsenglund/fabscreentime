import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

// Types mirror the Go JSON contract (internal/server, internal/shared).
export interface DeviceSummary {
  device_uuid: string;
  name: string;
  hostname: string;
  last_seen: number;
  agent_version: string;
  monitor_minutes: number;
  active_minutes: number;
  session_minutes: number;
  sample_count: number;
}

export interface DeviceStatus {
  device_uuid: string;
  name: string;
  hostname: string;
  status: "pending" | "active" | "expired" | "revoked";
  last_seen: number;
  agent_version: string;
  agent_build: number;
  enrolled_at: number;
  log_titles: boolean;
  clock_skew: number;
  clock_skew_known: boolean;
}

export interface LatestAgent {
  version: string;
  build: number;
}

export interface DeviceEvent {
  ts: number;
  kind: string; // "updated" | "downgrade"
  detail: string;
}

export interface TrendPoint {
  day: string;
  monitor_minutes: number;
  active_minutes: number;
}

export interface HourBucket {
  hour: number;
  monitor_minutes: number;
  active_minutes: number;
}

export interface AppStat {
  exe: string;
  monitor_minutes: number;
}

export interface SignalDay {
  day: string;
  monitor_minutes: number;
  active_minutes: number;
  macro_minutes: number;
  session_minutes: number;
}

export interface HeatDay {
  day: string;
  hours: number[];
}

export interface PrepareEnrollResponse {
  device_uuid: string;
  enroll_secret: string;
  expires_in: number;
}

async function getJSON<T>(url: string): Promise<T> {
  const r = await fetch(url);
  if (!r.ok) throw new Error(`${url} → ${r.status}`);
  return r.json() as Promise<T>;
}

async function sendJSON<T>(url: string, method: string, body: unknown): Promise<T> {
  const r = await fetch(url, {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error(`${url} → ${r.status}`);
  return r.json() as Promise<T>;
}

const LIVE = 15_000; // dashboard is read-mostly; poll gently

export function useSummary(range: string) {
  return useQuery({
    queryKey: ["summary", range],
    queryFn: () => getJSON<{ devices: DeviceSummary[] }>(`/api/dashboard/summary?range=${range}`),
    refetchInterval: LIVE,
  });
}

export function useDevices() {
  return useQuery({
    queryKey: ["devices"],
    queryFn: () => getJSON<{ devices: DeviceStatus[]; latest: LatestAgent | null }>(`/api/devices`),
    refetchInterval: LIVE,
  });
}

export function useDevice(uuid: string) {
  return useQuery({
    queryKey: ["device", uuid],
    queryFn: () => getJSON<DeviceStatus>(`/api/devices/${uuid}`),
    refetchInterval: LIVE,
  });
}

export function useTrend(range: string) {
  return useQuery({
    queryKey: ["trend", range],
    queryFn: () => getJSON<{ trend: TrendPoint[] }>(`/api/stats/trend?range=${range}`),
    refetchInterval: LIVE,
  });
}

export function useTimeline(uuid: string, day: string) {
  return useQuery({
    queryKey: ["timeline", uuid, day],
    queryFn: () => getJSON<{ day: string; hours: HourBucket[] }>(`/api/devices/${uuid}/timeline?day=${day}`),
    refetchInterval: LIVE,
  });
}

export function useTopApps(uuid: string, range: string) {
  return useQuery({
    queryKey: ["top-apps", uuid, range],
    queryFn: () => getJSON<{ apps: AppStat[] }>(`/api/devices/${uuid}/top-apps?range=${range}&limit=10`),
    refetchInterval: LIVE,
  });
}

export function useSignals(uuid: string, range: string) {
  return useQuery({
    queryKey: ["signals", uuid, range],
    queryFn: () => getJSON<{ signals: SignalDay[] }>(`/api/devices/${uuid}/signals?range=${range}`),
    refetchInterval: LIVE,
  });
}

export function useDeviceEvents(uuid: string) {
  return useQuery({
    queryKey: ["events", uuid],
    queryFn: () => getJSON<{ events: DeviceEvent[] }>(`/api/devices/${uuid}/events`),
    refetchInterval: LIVE,
  });
}

export function useHeatmap(uuid: string, days = 14) {
  return useQuery({
    queryKey: ["heatmap", uuid, days],
    queryFn: () => getJSON<{ heatmap: HeatDay[] }>(`/api/devices/${uuid}/heatmap?days=${days}`),
    refetchInterval: LIVE,
  });
}

export function usePrepareEnroll() {
  return useMutation({
    mutationFn: (name: string) =>
      sendJSON<PrepareEnrollResponse>(`/api/enroll/prepare`, "POST", { name }),
  });
}

export function usePatchDevice() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { uuid: string; name?: string; revoked?: boolean; log_titles?: boolean }) =>
      sendJSON<DeviceStatus>(`/api/devices/${v.uuid}`, "PATCH", {
        name: v.name,
        revoked: v.revoked,
        log_titles: v.log_titles,
      }),
    onSuccess: (data, v) => {
      // Seed the single-device cache with the fresh status the PATCH returned, so
      // the UI reflects the change immediately instead of briefly showing the old
      // value until the background refetch lands.
      qc.setQueryData(["device", v.uuid], data);
      qc.invalidateQueries({ queryKey: ["devices"] });
      qc.invalidateQueries({ queryKey: ["summary"] });
      qc.invalidateQueries({ queryKey: ["device", v.uuid] });
    },
  });
}

async function del(url: string): Promise<void> {
  const r = await fetch(url, { method: "DELETE" });
  if (!r.ok) throw new Error(`${url} → ${r.status}`);
}

export function useDeleteDevice() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (uuid: string) => del(`/api/devices/${uuid}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["devices"] });
      qc.invalidateQueries({ queryKey: ["summary"] });
    },
  });
}

/** manifestSHA256 fetches the served agent's expected hash, or "" if no release
 *  is published (the installer then skips the integrity check). */
export async function manifestSHA256(): Promise<string> {
  try {
    const r = await fetch("/agent/manifest");
    if (!r.ok) return "";
    const m = await r.json();
    return m?.manifest?.sha256 ?? "";
  } catch {
    return "";
  }
}

/** Build a double-clickable .bat installer client-side so the one-time secret
 *  lives in the file BODY, never a URL (PLAN.md §7.3). It uses only tools built
 *  into Windows 10+ (curl, certutil) — no PowerShell, no terminal: the user
 *  double-clicks it. curl-downloaded files carry no Mark-of-the-Web, so the
 *  agent itself runs without a SmartScreen prompt; only the .bat does.
 *
 *  Right before the agent first runs it adds a Defender folder-exclusion so a
 *  behavioral antivirus block (the documented failure mode, §9) can't quarantine
 *  it seconds after start. Only that one step elevates (a one-shot RunAs
 *  PowerShell) — the download and install stay unelevated so the agent's task and
 *  DPAPI token bind to the logged-in user, not whoever approved the prompt. It
 *  then waits ~15s and confirms the agent is still running, printing the
 *  third-party-AV fix if something else blocked it. */
export function buildBatInstaller(server: string, secret: string, sha: string): string {
  const lines = [
    "@echo off",
    "setlocal enabledelayedexpansion",
    "title FabScreenTime setup",
    `set "SERVER=${server}"`,
    `set "SECRET=${secret}"`,
    `set "SHA=${sha}"`,
    'set "DIR=%ProgramData%\\FabScreenTime"',
    "echo(",
    "echo   Setting up the FabScreenTime agent on this PC...",
    "echo(",
    'if not exist "%DIR%" mkdir "%DIR%" 2>nul',
    "",
    "rem Reuse an already-correct agent, so re-running is safe: a running agent",
    "rem locks its own .exe and a fresh download would fail to overwrite it.",
    "if not defined SHA goto :download",
    'if not exist "%DIR%\\agent.exe" goto :download',
    'set "HAVE="',
    `for /f "skip=1 delims=" %%H in ('certutil -hashfile "%DIR%\\agent.exe" SHA256 2^>nul') do if not defined HAVE set "HAVE=%%H"`,
    'set "HAVE=!HAVE: =!"',
    'if /I "!HAVE!"=="%SHA%" goto :reuse',
    "goto :download",
    "",
    ":reuse",
    "echo   - agent already present, reusing it",
    "goto :enroll",
    "",
    ":download",
    "echo   - downloading agent",
    'curl -fsS -o "%DIR%\\agent.exe" "%SERVER%/agent/download"',
    "if errorlevel 1 goto :fail_dl",
    "if not defined SHA goto :enroll",
    "echo   - verifying download",
    'set "GOT="',
    `for /f "skip=1 delims=" %%H in ('certutil -hashfile "%DIR%\\agent.exe" SHA256 2^>nul') do if not defined GOT set "GOT=%%H"`,
    'set "GOT=!GOT: =!"',
    'if /I not "!GOT!"=="%SHA%" goto :fail_hash',
    "",
    ":enroll",
    "echo   - enrolling this device",
    '> "%DIR%\\enroll.json" echo {"server":"%SERVER%","enroll_secret":"%SECRET%"}',
    "",
    "rem Everything above ran as the normal user. Now — and only now, right before",
    "rem the agent first runs — ask Windows Defender to allow the agent's folder, so",
    "rem a behavioral block can't quarantine it seconds after it starts (the common",
    "rem failure on a new PC). This is the ONLY step that needs admin: elevate just a",
    "rem one-shot PowerShell for it and leave the install itself unelevated, so the",
    "rem agent's task and DPAPI token stay bound to THIS user, not whoever approved",
    "rem the prompt. It must come BEFORE -install (which launches the agent), not",
    "rem after — the block is faster than anyone can click a UAC prompt. Best-effort:",
    "rem if admin is declined or a third-party AV is used, we carry on and the",
    "rem liveness check below explains what to do.",
    "echo   - allowing the agent in Windows Defender (asks for admin once)",
    `> "%DIR%\\allow.ps1" echo try { Add-MpPreference -ExclusionPath '%DIR%' -ErrorAction Stop } catch { }`,
    `powershell -NoProfile -Command "try { Start-Process powershell.exe -Verb RunAs -Wait -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-WindowStyle','Hidden','-File','%DIR%\\allow.ps1' } catch { }"`,
    'del "%DIR%\\allow.ps1" >nul 2>&1',
    "echo   - installing background agent",
    '"%DIR%\\agent.exe" -install -server "%SERVER%"',
    "if errorlevel 1 goto :fail_install",
    "",
    "rem A behavioral antivirus block (Defender's most common reaction to a new,",
    "rem unsigned, self-hiding agent) deletes agent.exe and kills the process a few",
    "rem seconds after it starts. Wait, then confirm it is actually still running so",
    "rem we don't claim success for an agent that was silently removed.",
    "echo   - checking it stays running (antivirus can quarantine new background apps)",
    "timeout /t 15 /nobreak >nul",
    'tasklist /FI "IMAGENAME eq agent.exe" /NH 2>nul | find /I "agent.exe" >nul || goto :blocked',
    "echo(",
    "echo   All set. This PC will appear on the dashboard within a minute.",
    "echo   You can close this window.",
    "timeout /t 8 >nul",
    "exit /b 0",
    "",
    ":blocked",
    "echo(",
    "echo   Almost there - but the agent is NOT running. An antivirus most likely",
    "echo   removed it:",
    "echo(",
    "echo    - Windows Defender: we tried to allow it automatically. If you clicked",
    "echo      No on the administrator prompt, just run this installer again and",
    "echo      click Yes.",
    "echo    - A different antivirus (Norton, Avast, McAfee, ...): open it, allow",
    "echo      this folder, then run this installer again:",
    "echo         %DIR%",
    "echo(",
    "echo   This PC is already enrolled, so it just reconnects - no need to re-add",
    "echo   it. No antivirus involved? Open %DIR%\\agent.log for a different error.",
    "pause",
    "exit /b 1",
    "",
    ":fail_dl",
    "echo(",
    "echo   ERROR: could not download the agent from %SERVER%",
    "echo   - If you are reinstalling, the agent may already be running (its file is",
    "echo     locked). The existing install is fine, or restart the PC and try again.",
    "echo   - Otherwise, make sure this PC can reach the server and run this again.",
    "pause",
    "exit /b 1",
    "",
    ":fail_hash",
    "echo(",
    "echo   ERROR: the downloaded agent failed verification. Aborting for safety.",
    "pause",
    "exit /b 1",
    "",
    ":fail_install",
    "echo(",
    "echo   ERROR: install failed. See %DIR%\\agent.log for details.",
    "pause",
    "exit /b 1",
  ];
  return lines.join("\r\n") + "\r\n";
}
