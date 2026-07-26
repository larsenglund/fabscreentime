import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Check, ChevronLeft, ChevronRight, Loader2, Pencil } from "lucide-react";
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
  const rename = usePatchDevice();

  const hours = timeline.data?.hours ?? [];
  const dayMonitor = hours.reduce((s, h) => s + h.monitor_minutes, 0);
  const dayActive = hours.reduce((s, h) => s + h.active_minutes, 0);
  const d = device.data;
  const isToday = day === todayUTC();

  // Average screen-on / in-use time per day over the last week. The signals series
  // is zero-filled server-side (one row per calendar day), so dividing the total
  // by its length gives a true per-day average that counts idle days too.
  const weekSignals = signals.data?.signals ?? [];
  const avgDailyMonitor = weekSignals.length
    ? weekSignals.reduce((sum, s) => sum + s.monitor_minutes, 0) / weekSignals.length
    : 0;
  const avgDailyActive = weekSignals.length
    ? weekSignals.reduce((sum, s) => sum + s.active_minutes, 0) / weekSignals.length
    : 0;

  const [editingName, setEditingName] = useState(false);
  const [draftName, setDraftName] = useState("");
  const pencilRef = useRef<HTMLButtonElement>(null);
  const wasEditing = useRef(false);

  // Return focus to the rename (pencil) trigger when the editor closes, so a
  // keyboard/screen-reader user doesn't get dropped to <body>. Guarded so it
  // doesn't steal focus on first mount.
  useEffect(() => {
    if (wasEditing.current && !editingName) pencilRef.current?.focus();
    wasEditing.current = editingName;
  }, [editingName]);

  function startRename() {
    setDraftName(d?.name ?? "");
    rename.reset();
    setEditingName(true);
  }
  function submitRename() {
    if (rename.isPending) return; // ignore a second Enter while a save is in flight
    const name = draftName.trim();
    if (!name) return; // a computer must keep a name; empty falls back to hostname anyway
    if (name === (d?.name ?? "")) {
      setEditingName(false);
      return;
    }
    rename.mutate({ uuid, name }, { onSuccess: () => setEditingName(false) });
  }

  return (
    <div className="space-y-6">
      <div>
        <Link to="/" className="mb-3 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
          <ChevronLeft className="size-4" /> Overview
        </Link>
        <div className="flex flex-wrap items-center gap-3">
          {d && editingName ? (
            <div className="flex flex-wrap items-center gap-2">
              <input
                autoFocus
                value={draftName}
                onChange={(e) => setDraftName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") submitRename();
                  if (e.key === "Escape") setEditingName(false);
                }}
                onFocus={(e) => e.target.select()}
                maxLength={64}
                aria-label="Computer name"
                aria-invalid={rename.isError}
                aria-describedby={rename.isError ? "rename-error" : undefined}
                className="h-9 w-56 max-w-full rounded-lg border bg-background px-3 text-xl font-semibold tracking-tight outline-none focus-visible:border-primary"
              />
              <Button size="sm" onClick={submitRename} disabled={rename.isPending || !draftName.trim()}>
                {rename.isPending ? <Loader2 className="size-4 animate-spin" /> : <Check className="size-4" />}
                Save
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setEditingName(false)} disabled={rename.isPending}>
                Cancel
              </Button>
            </div>
          ) : (
            <>
              <h1 className="text-xl font-semibold tracking-tight">
                {d ? d.name || d.hostname || uuid.slice(0, 8) : <Skeleton className="h-6 w-40" />}
              </h1>
              {d && (
                <button
                  ref={pencilRef}
                  onClick={startRename}
                  aria-label="Rename this computer"
                  title="Rename this computer"
                  className="-m-1 rounded p-1 text-muted-foreground transition-colors hover:text-foreground"
                >
                  <Pencil className="size-4" />
                </button>
              )}
              {d && <StatusBadge device={d} />}
            </>
          )}
        </div>
        {editingName && rename.isError && (
          <p id="rename-error" role="alert" className="mt-1.5 text-xs text-danger">
            Couldn't rename. Try again.
          </p>
        )}
        {d && (
          <div className="mt-1 flex flex-wrap items-center gap-x-1 gap-y-1 text-sm text-muted-foreground">
            <span>
              {d.hostname && <>{d.hostname} · </>}
              {d.agent_version && <>app v{d.agent_version} · </>}
              {d.enrolled_at > 0 && <>added {fmtDate(d.enrolled_at)} · </>}
              {ago(d.last_seen)}
            </span>
            {d.clock_skew_known && Math.abs(d.clock_skew) >= CLOCK_SKEW_FLAG_SECONDS && (
              <span
                className="rounded bg-rose-500/15 px-1.5 py-0.5 text-xs font-medium text-rose-600 dark:text-rose-400"
                title="This computer's clock is off by too much, so its activity gets logged at the wrong times. Check its time settings (in Windows, turn on Set time automatically / Internet Time)."
              >
                clock {fmtClockSkew(d.clock_skew)}
              </span>
            )}
          </div>
        )}
      </div>

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
        <Kpi label="Screen on · day" value={fmtMinutes(dayMonitor)} />
        <Kpi label="In use · day" value={fmtMinutes(dayActive)} />
        <Kpi
          label="Idle while on"
          value={fmtMinutes(Math.max(0, dayMonitor - dayActive))}
          sub="on, no input"
        />
        <Kpi
          label="Avg screen on / day"
          value={signals.isLoading ? <Skeleton className="h-7 w-16" /> : fmtMinutes(avgDailyMonitor)}
          sub="last 7 days"
        />
        <Kpi
          label="Avg in use / day"
          value={signals.isLoading ? <Skeleton className="h-7 w-16" /> : fmtMinutes(avgDailyActive)}
          sub="last 7 days"
        />
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
          The solid bars show when the screen was switched on — that's the time we count as
          screentime. The lighter overlay shows when someone was actually using the computer; the gap
          between them is time the screen was on but sitting idle.
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
        title="Screen on vs. in use"
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
        action={<span className="text-xs text-muted-foreground">screen on · last 14 days</span>}
      >
        {heatmap.isLoading ? (
          <Skeleton className="h-48 w-full" />
        ) : (
          <Heatmap days={heatmap.data?.heatmap ?? []} />
        )}
      </CardSection>

      <CardSection
        title="Update history"
        action={<span className="text-xs text-muted-foreground">the app updates itself</span>}
      >
        {eventsQ.isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : !eventsQ.data?.events.length ? (
          <p className="text-sm text-muted-foreground">
            No updates yet. Each time the FabScreenTime app on this computer updates itself, it'll
            show up here — and we'll flag it if it ever goes back to an <em>older</em> version
            instead of a newer one.
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
              When this is off, we only keep which app was open and when — not the window titles.
              Titles can reveal web addresses, document names, and contacts, so they're removed
              before anything is saved.
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
