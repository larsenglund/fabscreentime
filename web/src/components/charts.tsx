import { Link } from "react-router-dom";
import type { AppStat, DeviceSummary, HourBucket, TrendPoint } from "../lib/api";
import { fmtMinutes } from "../lib/format";
import { Muted } from "./ui/skeleton";

const barTrack = "h-2.5 overflow-hidden rounded-full bg-muted";

/** DeviceBars ranks devices by monitor-on minutes. Each bar shows monitor-on
 *  (solid) with input-active drawn over it, so the idle gap is visible. */
export function DeviceBars({ devices }: { devices: DeviceSummary[] }) {
  const rows = [...devices]
    .filter((d) => d.sample_count > 0 || d.monitor_minutes > 0)
    .sort((a, b) => b.monitor_minutes - a.monitor_minutes);
  if (!rows.length) return <Muted>No screentime recorded in this range yet.</Muted>;
  const max = Math.max(1, ...rows.map((d) => d.monitor_minutes));
  return (
    <div className="space-y-3.5">
      {rows.map((d) => (
        <div key={d.device_uuid}>
          <div className="mb-1.5 flex items-baseline justify-between gap-3 text-sm">
            <Link
              to={`/devices/${d.device_uuid}`}
              className="truncate font-medium hover:text-primary hover:underline"
            >
              {d.name || d.hostname || d.device_uuid.slice(0, 8)}
            </Link>
            <span className="tnum shrink-0 text-muted-foreground">{fmtMinutes(d.monitor_minutes)}</span>
          </div>
          <div className={`relative ${barTrack}`}>
            <div
              className="absolute inset-y-0 left-0 rounded-full bg-monitor"
              style={{ width: `${(d.monitor_minutes / max) * 100}%` }}
            />
            <div
              className="absolute inset-y-0 left-0 rounded-full bg-active"
              style={{ width: `${(d.active_minutes / max) * 100}%`, opacity: 0.85 }}
            />
          </div>
        </div>
      ))}
    </div>
  );
}

/** TrendArea plots household daily monitor-on minutes as a filled area, with a
 *  dashed input-active line beneath. */
export function TrendArea({ points }: { points: TrendPoint[] }) {
  const W = 600;
  const H = 140;
  const pad = 8;
  const n = points.length;
  if (!n) return <Muted>No daily rollups yet.</Muted>;
  const max = Math.max(1, ...points.map((p) => p.monitor_minutes));
  const x = (i: number) => (n <= 1 ? W / 2 : pad + (i / (n - 1)) * (W - 2 * pad));
  const y = (v: number) => H - pad - (v / max) * (H - 2 * pad);
  const path = (key: "monitor_minutes" | "active_minutes") =>
    points.map((p, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(p[key]).toFixed(1)}`).join(" ");
  const area = `${path("monitor_minutes")} L${x(n - 1).toFixed(1)},${H - pad} L${x(0).toFixed(1)},${H - pad} Z`;
  return (
    <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="h-32 w-full">
      <path d={area} className="fill-monitor" opacity={0.14} />
      <path d={path("monitor_minutes")} className="stroke-monitor" fill="none" strokeWidth={2} vectorEffect="non-scaling-stroke" />
      <path
        d={path("active_minutes")}
        className="stroke-active"
        fill="none"
        strokeWidth={1.5}
        strokeDasharray="3 3"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}

/** HourStrip shows a device's 24-hour day: monitor-on bars with input-active
 *  overlaid, reading like a sleep tracker. */
export function HourStrip({ hours }: { hours: HourBucket[] }) {
  const max = Math.max(1, ...hours.map((h) => h.monitor_minutes));
  return (
    <div>
      <div className="flex h-28 items-end gap-[3px]">
        {hours.map((h) => (
          <div
            key={h.hour}
            className="relative h-full flex-1"
            title={`${String(h.hour).padStart(2, "0")}:00 — ${h.monitor_minutes}m on, ${h.active_minutes}m active`}
          >
            <div
              className="absolute inset-x-0 bottom-0 rounded-sm bg-monitor"
              style={{ height: `${(h.monitor_minutes / max) * 100}%` }}
            />
            <div
              className="absolute inset-x-0 bottom-0 rounded-sm bg-active"
              style={{ height: `${(h.active_minutes / max) * 100}%`, opacity: 0.8 }}
            />
          </div>
        ))}
      </div>
      <div className="mt-1.5 flex justify-between text-[10px] text-muted-foreground">
        {["00", "06", "12", "18", "24"].map((t) => (
          <span key={t}>{t}</span>
        ))}
      </div>
    </div>
  );
}

/** TopApps ranks foreground apps by monitor-on minutes. */
export function TopApps({ apps }: { apps: AppStat[] }) {
  if (!apps.length) return <Muted>No app data in this range yet.</Muted>;
  const max = Math.max(1, ...apps.map((a) => a.monitor_minutes));
  return (
    <div className="space-y-2.5">
      {apps.map((a) => (
        <div key={a.exe}>
          <div className="mb-1 flex items-baseline justify-between gap-3 text-sm">
            <span className="truncate">{a.exe}</span>
            <span className="tnum shrink-0 text-muted-foreground">{fmtMinutes(a.monitor_minutes)}</span>
          </div>
          <div className={barTrack}>
            <div
              className="h-full rounded-full bg-primary"
              style={{ width: `${(a.monitor_minutes / max) * 100}%` }}
            />
          </div>
        </div>
      ))}
    </div>
  );
}

/** SignalLegend explains the two series wherever they appear together. */
export function SignalLegend() {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
      <span className="inline-flex items-center gap-1.5">
        <span className="size-2.5 rounded-sm bg-monitor" /> Monitor-on
      </span>
      <span className="inline-flex items-center gap-1.5">
        <span className="size-2.5 rounded-sm bg-active" /> Input-active
      </span>
    </div>
  );
}
