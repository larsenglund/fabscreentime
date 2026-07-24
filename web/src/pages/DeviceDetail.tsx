import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { useDevice, useTimeline, useTopApps } from "../lib/api";
import { fmtMinutes, ago, todayUTC, shiftDay } from "../lib/format";
import { Kpi } from "../components/Kpi";
import { CardSection } from "../components/ui/card";
import { StatusBadge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Skeleton } from "../components/ui/skeleton";
import { HourStrip, TopApps, SignalLegend } from "../components/charts";
import { RangeSwitcher, type RangeKey } from "../components/RangeSwitcher";

export function DeviceDetail() {
  const { uuid = "" } = useParams();
  const [day, setDay] = useState(todayUTC());
  const [range, setRange] = useState<RangeKey>("30d");
  const device = useDevice(uuid);
  const timeline = useTimeline(uuid, day);
  const topApps = useTopApps(uuid, range);

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
          <div className="mt-1 text-sm text-muted-foreground">
            {d.hostname && <span>{d.hostname} · </span>}
            {d.agent_version && <span>agent {d.agent_version} · </span>}
            <span>{ago(d.last_seen)}</span>
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
    </div>
  );
}
