import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ChevronLeft, ChevronRight } from "lucide-react";
import {
  useDevice,
  useDeviceEvents,
  useHeatmap,
  usePatchDevice,
  useSignals,
  useTimeline,
  useTopApps,
} from "../lib/api";
import {
  fmtMinutes,
  ago,
  todayUTC,
  shiftDay,
  fmtClockSkew,
  fmtDate,
  CLOCK_SKEW_FLAG_SECONDS,
} from "../lib/format";
import { Kpi } from "../components/Kpi";
import { CardSection } from "../components/ui/card";
import { StatusBadge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Switch } from "../components/ui/switch";
import { Skeleton } from "../components/ui/skeleton";
import { HourStrip, TopApps, SignalLegend, SignalComparison, Heatmap } from "../components/charts";
import { RangeSwitcher, type RangeKey } from "../components/RangeSwitcher";

export function DeviceDetail() {
  const { uuid = "" } = useParams();
  const [day, setDay] = useState(todayUTC());
  const [range, setRange] = useState<RangeKey>("30d");
  const device = useDevice(uuid);
  const timeline = useTimeline(uuid, day);
  const topApps = useTopApps(uuid, range);
  const signals = useSignals(uuid, "7d");
  const heatmap = useHeatmap(uuid, 14);
  const eventsQ = useDeviceEvents(uuid);
  const patch = usePatchDevice();

  const hours = timeline.data?.hours ?? [];
  const dayMonitor = hours.reduce((s, h) => s + h.monitor_minutes, 0);
  const dayActive = hours.reduce((s, h) => s + h.active_minutes, 0);
  const d = device.data;
  const isToday = day === todayUTC();

  return (
    <div className="space-y-6">
      <div>
        <Link to="/" className="mb-3 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
          <ChevronLeft className="size-4" /> Overview
        </Link>
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="text-xl font-semibold tracking-tight">
            {d ? d.name || d.hostname || uuid.slice(0, 8) : <Skeleton className="h-6 w-40" />}
          </h1>
          {d && <StatusBadge device={d} />}
        </div>
        {d && (
          <div className="mt-1 flex flex-wrap items-center gap-x-1 gap-y-1 text-sm text-muted-foreground">
            <span>
              {d.hostname && <>{d.hostname} · </>}
              {d.agent_version && <>agent {d.agent_version} · </>}
              {d.enrolled_at > 0 && <>enrolled {fmtDate(d.enrolled_at)} · </>}
              {ago(d.last_seen)}
            </span>
            {d.clock_skew_known && Math.abs(d.clock_skew) >= CLOCK_SKEW_FLAG_SECONDS && (
              <span
                className="rounded bg-rose-500/15 px-1.5 py-0.5 text-xs font-medium text-rose-600 dark:text-rose-400"
                title="Large clock skew distorts when this device's activity is recorded. Check its time sync (e.g. Windows Internet Time)."
              >
                clock {fmtClockSkew(d.clock_skew)}
              </span>
            )}
          </div>
        )}
      </div>

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Kpi label="Monitor-on · day" value={fmtMinutes(dayMonitor)} />
        <Kpi label="Input-active · day" value={fmtMinutes(dayActive)} />
        <Kpi
          label="Idle while on"
          value={fmtMinutes(Math.max(0, dayMonitor - dayActive))}
          sub="on, no input"
        />
        <Kpi label="Status" value={d ? d.status : "—"} />
      </div>

      <CardSection
        title="Daily timeline"
        action={
          <div className="flex items-center gap-2">
            <Button variant="ghost" size="icon" onClick={() => setDay(shiftDay(day, -1))} aria-label="Previous day">
              <ChevronLeft className="size-4" />
            </Button>
            <span className="tnum w-24 text-center text-sm">{day}</span>
            <Button
              variant="ghost"
              size="icon"
              onClick={() => !isToday && setDay(shiftDay(day, 1))}
              disabled={isToday}
              aria-label="Next day"
            >
              <ChevronRight className="size-4" />
            </Button>
          </div>
        }
      >
        <div className="mb-3">
          <SignalLegend />
        </div>
        {timeline.isLoading ? <Skeleton className="h-28 w-full" /> : <HourStrip hours={hours} />}
        <p className="mt-4 text-xs text-muted-foreground">
          The solid bars are monitor-on minutes — the primary “screentime” signal. The lighter
          overlay is input-active minutes; the gap between them is time the screen was on but idle.
        </p>
      </CardSection>

      <CardSection
        title="Top apps"
        action={<RangeSwitcher value={range} onChange={setRange} />}
      >
        {topApps.isLoading ? (
          <Skeleton className="h-40 w-full" />
        ) : (
          <TopApps apps={topApps.data?.apps ?? []} />
        )}
      </CardSection>

      <CardSection
        title="Signal comparison"
        action={<span className="text-xs text-muted-foreground">last 7 days</span>}
      >
        {signals.isLoading ? (
          <Skeleton className="h-40 w-full" />
        ) : (
          <SignalComparison days={signals.data?.signals ?? []} />
        )}
      </CardSection>

      <CardSection
        title="Activity heatmap"
        action={<span className="text-xs text-muted-foreground">monitor-on · last 14 days</span>}
      >
        {heatmap.isLoading ? (
          <Skeleton className="h-48 w-full" />
        ) : (
          <Heatmap days={heatmap.data?.heatmap ?? []} />
        )}
      </CardSection>

      <CardSection
        title="Update history"
        action={<span className="text-xs text-muted-foreground">agent self-updates</span>}
      >
        {eventsQ.isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : !eventsQ.data?.events.length ? (
          <p className="text-sm text-muted-foreground">
            No agent updates recorded yet. Entries appear here whenever this device's agent build
            changes — a build moving <em>backwards</em> is flagged as a downgrade.
          </p>
        ) : (
          <ol className="space-y-2">
            {eventsQ.data.events.map((e, i) => (
              <li key={i} className="flex items-baseline gap-2 text-sm">
                <span
                  className={
                    "shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium " +
                    (e.kind === "downgrade"
                      ? "bg-rose-500/15 text-rose-600 dark:text-rose-400"
                      : "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400")
                  }
                >
                  {e.kind}
                </span>
                <span className="min-w-0 flex-1 break-words">{e.detail}</span>
                <span className="tnum shrink-0 text-xs text-muted-foreground">{ago(e.ts)}</span>
              </li>
            ))}
          </ol>
        )}
      </CardSection>

      <CardSection title="Privacy">
        <div className="flex items-center justify-between gap-4">
          <div>
            <div className="text-sm font-medium">Log window titles</div>
            <div className="mt-0.5 max-w-md text-xs text-muted-foreground">
              When off, only the app (.exe) and activity are recorded — window titles (which can
              reveal URLs, documents, and contacts) are dropped server-side before storage.
            </div>
          </div>
          <Switch
            checked={d?.log_titles ?? true}
            disabled={!d || patch.isPending}
            onChange={(v) => patch.mutate({ uuid, log_titles: v })}
          />
        </div>
      </CardSection>
    </div>
  );
}
