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
  enrolled_at: number;
  log_titles: boolean;
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
    queryFn: () => getJSON<{ devices: DeviceStatus[] }>(`/api/devices`),
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
    onSuccess: (_data, v) => {
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
 *  agent itself runs without a SmartScreen prompt; only the .bat does. */
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
    "echo   - installing background agent",
    '"%DIR%\\agent.exe" -install -server "%SERVER%"',
    "if errorlevel 1 goto :fail_install",
    "echo(",
    "echo   All set. This PC will appear on the dashboard within a minute.",
    "echo   You can close this window.",
    "timeout /t 8 >nul",
    "exit /b 0",
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
