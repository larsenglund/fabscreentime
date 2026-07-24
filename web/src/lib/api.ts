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

export function usePrepareEnroll() {
  return useMutation({
    mutationFn: (name: string) =>
      sendJSON<PrepareEnrollResponse>(`/api/enroll/prepare`, "POST", { name }),
  });
}

export function usePatchDevice() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { uuid: string; name?: string; revoked?: boolean }) =>
      sendJSON<DeviceStatus>(`/api/devices/${v.uuid}`, "PATCH", { name: v.name, revoked: v.revoked }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["devices"] });
      qc.invalidateQueries({ queryKey: ["summary"] });
    },
  });
}

/** Build the personalized silent installer (PowerShell) client-side so the
 *  one-time secret lives in the file BODY, never a URL (PLAN.md §7.3). */
export function buildInstaller(server: string, secret: string): string {
  return [
    "$ErrorActionPreference = 'Stop'",
    `$Server = '${server}'`,
    `$Secret = '${secret}'`,
    "$dir = Join-Path $env:LOCALAPPDATA 'FabScreenTime'",
    "New-Item -ItemType Directory -Force -Path $dir | Out-Null",
    "$exe = Join-Path $dir 'agent.exe'",
    'Write-Host "Downloading agent from $Server ..."',
    'Invoke-WebRequest -UseBasicParsing -Uri "$Server/agent/download" -OutFile $exe',
    "try {",
    '  $m = Invoke-RestMethod -UseBasicParsing -Uri "$Server/agent/manifest"',
    "  $h = (Get-FileHash -Algorithm SHA256 $exe).Hash.ToLower()",
    '  if ($m.manifest.sha256 -and $m.manifest.sha256 -ne $h) { throw "agent.exe hash mismatch" }',
    '} catch { Write-Warning "manifest check skipped: $_" }',
    "(@{ server = $Server; enroll_secret = $Secret } | ConvertTo-Json) | Set-Content -Path (Join-Path $dir 'enroll.json') -Encoding UTF8",
    "& $exe -install -server $Server",
    "Write-Host 'Installed. It should appear on the dashboard within a minute.'",
  ].join("\r\n");
}
