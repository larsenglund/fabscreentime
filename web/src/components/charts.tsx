import { Link } from "react-router-dom";
import type { AppStat, DeviceSummary, HeatDay, HourBucket, SignalDay, TrendPoint } from "../lib/api";
import { fmtMinutes, fmtDayShort, isWeekend } from "../lib/format";
import { cn } from "../lib/cn";
import { Muted } from "./ui/skeleton";

/** weekendLabel emphasises Saturday/Sunday day labels so weekends stand out in
 *  the day-based views; weekdays stay muted. */
const weekendLabel = (day: string) =>
  isWeekend(day) ? "font-semibold text-foreground" : "text-muted-foreground";

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
  if (!n) return <Muted>No daily totals yet.</Muted>;
  const max = Math.max(1, ...points.map((p) => p.monitor_minutes));
  const x = (i: number) => (n <= 1 ? W / 2 : pad + (i / (n - 1)) * (W - 2 * pad));
  const y = (v: number) => H - pad - (v / max) * (H - 2 * pad);
  const path = (key: "monitor_minutes" | "active_minutes") =>
    points.map((p, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(p[key]).toFixed(1)}`).join(" ");
  const area = `${path("monitor_minutes")} L${x(n - 1).toFixed(1)},${H - pad} L${x(0).toFixed(1)},${H - pad} Z`;
  // Weekend shading: each band reaches halfway to its neighbours so consecutive
  // weekend days tile into one block, with the outer edges flush to the chart.
  const weekendBands = points.map((p, i) => {
    if (!isWeekend(p.day)) return null;
    const left = i === 0 ? 0 : (x(i - 1) + x(i)) / 2;
    const right = i === n - 1 ? W : (x(i) + x(i + 1)) / 2;
    return (
      <rect
        key={p.day}
        x={left}
        y={0}
        width={Math.max(0, right - left)}
        height={H}
        className="fill-muted-foreground"
        opacity={0.14}
      />
    );
  });
  return (
    <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="h-32 w-full">
      {weekendBands}
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
            title={`${String(h.hour).padStart(2, "0")}:00 — screen on ${h.monitor_minutes}m, in use ${h.active_minutes}m`}
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
      <LegendItem className="bg-monitor" label="Screen on" />
      <LegendItem className="bg-active" label="In use" />
    </div>
  );
}

function LegendItem({ className, label }: { className: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={`size-2.5 rounded-sm ${className}`} /> {label}
    </span>
  );
}

/** Heatmap renders days × hours, cell intensity ∝ monitor-on minutes — reads
 *  like a sleep tracker across the range (Tailwind divs, no chart lib). */
export function Heatmap({ days }: { days: HeatDay[] }) {
  const max = Math.max(1, ...days.flatMap((d) => d.hours));
  if (!days.length) return <Muted>No data in this range yet.</Muted>;
  return (
    <div className="overflow-x-auto">
      <div className="min-w-[540px]">
        {/* pl matches the row label column (w-16) + the 2px flex gap, so the hour
            headings stay aligned with the cells below. */}
        <div className="mb-1 flex gap-[2px] pl-[66px] text-[10px] text-muted-foreground">
          {Array.from({ length: 24 }).map((_, h) => (
            <div key={h} className="flex-1 text-center">
              {h % 6 === 0 ? h : ""}
            </div>
          ))}
        </div>
        {days.map((d) => (
          <div key={d.day} className="mb-[2px] flex items-center gap-[2px]">
            <div className={cn("w-16 shrink-0 pr-1 text-right text-[10px]", weekendLabel(d.day))}>
              {fmtDayShort(d.day)}
            </div>
            {d.hours.map((v, h) => (
              <div
                key={h}
                className="aspect-square flex-1 rounded-[2px]"
                title={`${fmtDayShort(d.day)} ${String(h).padStart(2, "0")}:00 — screen on ${v}m`}
                style={{
                  backgroundColor: v
                    ? `rgb(var(--monitor) / ${(0.15 + 0.85 * Math.min(1, v / max)).toFixed(3)})`
                    : "rgb(var(--muted))",
                }}
              />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

/** SignalComparison shows, per day, screen-on vs. in-use minutes plus a third
 *  "active while the screen was off" bar — input recorded with the monitor
 *  physically off, the unattended/automation signal (PLAN.md §0.1) — flagged in
 *  red whenever it is nonzero. */
export function SignalComparison({ days }: { days: SignalDay[] }) {
  if (!days.some((d) => d.session_minutes > 0)) return <Muted>No activity recorded in this range yet.</Muted>;
  const max = Math.max(1, ...days.map((d) => Math.max(d.monitor_minutes, d.active_minutes, d.macro_minutes)));
  const anyMacro = days.some((d) => d.macro_minutes > 0);
  return (
    <div>
      {/* Rows carry their own vertical padding (for the weekend band), so the
          list gap is trimmed to keep the original rhythm. */}
      <div className="space-y-1">
        {days.map((d) => (
          // The negative margin lets the weekend band bleed past the bars without
          // shifting the row, so weekday and weekend rows stay aligned.
          <div
            key={d.day}
            className={cn(
              "-mx-2 flex items-center gap-3 rounded-md px-2 py-1",
              isWeekend(d.day) && "bg-muted/70",
            )}
          >
            <div className={cn("w-20 shrink-0 text-[11px]", weekendLabel(d.day))}>
              {fmtDayShort(d.day)}
            </div>
            <div className="flex-1 space-y-[3px]">
              <CmpBar value={d.monitor_minutes} max={max} className="bg-monitor" />
              <CmpBar value={d.active_minutes} max={max} className="bg-active" />
              {d.macro_minutes > 0 && <CmpBar value={d.macro_minutes} max={max} className="bg-danger" />}
            </div>
          </div>
        ))}
      </div>
      <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <LegendItem className="bg-monitor" label="Screen on" />
        <LegendItem className="bg-active" label="In use" />
        <LegendItem className="bg-danger" label="Active while screen off" />
      </div>
      {anyMacro && (
        <p className="mt-2 text-xs text-danger">
          The mouse or keyboard was active while the screen was switched off — usually a sign the
          computer was left running on its own, or driven by an automated script. This time doesn't
          count as screentime.
        </p>
      )}
    </div>
  );
}

function CmpBar({ value, max, className }: { value: number; max: number; className: string }) {
  return (
    <div className="h-1.5 overflow-hidden rounded-full bg-muted">
      <div className={`h-full rounded-full ${className}`} style={{ width: `${(value / max) * 100}%` }} />
    </div>
  );
}
