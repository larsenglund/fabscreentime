/** fmtMinutes renders a minute count as "0m" / "45m" / "2h 5m" / "3h". */
export function fmtMinutes(m: number): string {
  m = Math.max(0, Math.round(m || 0));
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  const mm = m % 60;
  return mm ? `${h}h ${mm}m` : `${h}h`;
}

/** fmtHours renders minutes as a compact decimal-hours string ("2.5h"). */
export function fmtHours(m: number): string {
  return `${((m || 0) / 60).toFixed(1)}h`;
}

/** ago renders a unix-seconds timestamp as a short relative string. */
export function ago(ts: number): string {
  if (!ts) return "never";
  const s = Math.max(0, Math.floor(Date.now() / 1000 - ts));
  if (s < 90) return `${s}s ago`;
  if (s < 5400) return `${Math.round(s / 60)}m ago`;
  if (s < 172800) return `${Math.round(s / 3600)}h ago`;
  return `${Math.round(s / 86400)}d ago`;
}

/** online is true if a device reported within the last 3 minutes. */
export function online(ts: number): boolean {
  return !!ts && Date.now() / 1000 - ts < 180;
}

/** CLOCK_SKEW_FLAG_SECONDS is the |skew| above which a device's clock is flagged.
 *  Well above normal upload latency + sample age, so only a genuinely wrong
 *  device clock trips it (§8). */
export const CLOCK_SKEW_FLAG_SECONDS = 120;

/** fmtClockSkew renders a signed clock offset (seconds) like "3m fast" / "45s
 *  slow" — positive means the device clock is ahead of the server. */
export function fmtClockSkew(sec: number): string {
  const a = Math.abs(Math.round(sec));
  const mag = a < 90 ? `${a}s` : a < 5400 ? `${Math.round(a / 60)}m` : `${Math.round(a / 3600)}h`;
  return `${mag} ${sec >= 0 ? "fast" : "slow"}`;
}

/** todayUTC returns today's date as YYYY-MM-DD in UTC (matches the backend's
 *  day boundaries). */
export function todayUTC(): string {
  return new Date().toISOString().slice(0, 10);
}

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

/** fmtDayShort renders a "YYYY-MM-DD" day as "Sun 26/7" — the weekday plus
 *  day/month reads far faster than a bare "07-26" on a chart axis. Parsed as UTC
 *  because the backend's day boundaries are UTC. */
export function fmtDayShort(day: string): string {
  const d = new Date(day + "T00:00:00Z");
  if (Number.isNaN(d.getTime())) return day;
  return `${WEEKDAYS[d.getUTCDay()]} ${d.getUTCDate()}/${d.getUTCMonth() + 1}`;
}

/** fmtDate renders a unix-seconds timestamp as a short absolute date
 *  ("20 Jul 2026"), for enrollment/registry context. */
export function fmtDate(ts: number): string {
  if (!ts) return "—";
  return new Date(ts * 1000).toLocaleDateString(undefined, {
    day: "numeric",
    month: "short",
    year: "numeric",
  });
}

/** shiftDay returns dayStr (YYYY-MM-DD) offset by n days. */
export function shiftDay(dayStr: string, n: number): string {
  const d = new Date(dayStr + "T00:00:00Z");
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
}
