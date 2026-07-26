import { useState } from "react";
import { Link } from "react-router-dom";
import { ChevronRight, Trash2 } from "lucide-react";
import {
  useDeleteDevice,
  useDevices,
  usePatchDevice,
  useSummary,
  useTrend,
  type DeviceSummary,
} from "../lib/api";
import { fmtMinutes, ago, online, fmtClockSkew, CLOCK_SKEW_FLAG_SECONDS } from "../lib/format";
import { RangeSwitcher, type RangeKey } from "../components/RangeSwitcher";
import { Kpi } from "../components/Kpi";
import { CardSection } from "../components/ui/card";
import { StatusBadge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Skeleton, Muted } from "../components/ui/skeleton";
import { DeviceBars, TrendArea, SignalLegend } from "../components/charts";

const rangeDays: Record<string, number> = { "24h": 1, "7d": 7, "30d": 30 };

export function Overview() {
  const [range, setRange] = useState<RangeKey>("7d");
  const summary = useSummary(range);
  const trend = useTrend(range);
  const devices = useDevices();
  const patch = usePatchDevice();
  const remove = useDeleteDevice();

  const devs = summary.data?.devices ?? [];
  const totalMonitor = devs.reduce((s, d) => s + d.monitor_minutes, 0);
  const reporting = devs.filter((d) => d.sample_count > 0).length;
  const onlineNow = (devices.data?.devices ?? []).filter((d) => online(d.last_seen)).length;
  const days = rangeDays[range] ?? 7;

  const metricByUuid = new Map(devs.map((d) => [d.device_uuid, d]));

  function revoke(uuid: string) {
    if (!confirm("Revoke this device? It will stop sending in activity until you set it up again."))
      return;
    patch.mutate({ uuid, revoked: true });
  }

  function remove_(uuid: string, label: string) {
    if (
      !confirm(
        `Delete “${label}” and all of its recorded data? This can't be undone.\n\n` +
          "If this is a real computer that still has FabScreenTime installed, uninstall it there " +
          "too, or it will keep trying to report.",
      )
    )
      return;
    remove.mutate(uuid);
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-xl font-semibold tracking-tight">Overview</h1>
        <RangeSwitcher value={range} onChange={setRange} />
      </div>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Kpi
          label={`Screentime · ${range === "24h" ? "today" : `last ${days}d`}`}
          value={summary.isLoading ? <Skeleton className="h-7 w-20" /> : fmtMinutes(totalMonitor)}
          sub="screen on, all devices"
        />
        <Kpi
          label="Daily average"
          value={summary.isLoading ? <Skeleton className="h-7 w-20" /> : fmtMinutes(totalMonitor / days)}
          sub="per day in range"
        />
        <Kpi label="Devices reporting" value={reporting} sub={`of ${devs.length} set up`} />
        <Kpi label="Online now" value={onlineNow} sub="reported under 3 min ago" />
      </div>

      <div className="grid gap-6 lg:grid-cols-5">
        <CardSection
          title="Screentime by device"
          action={<SignalLegend />}
          className="lg:col-span-3"
        >
          {summary.isLoading ? <Skeleton className="h-40 w-full" /> : <DeviceBars devices={devs} />}
        </CardSection>

        <CardSection title="Daily trend" className="lg:col-span-2">
          {trend.isLoading ? (
            <Skeleton className="h-32 w-full" />
          ) : (
            <TrendArea points={trend.data?.trend ?? []} />
          )}
        </CardSection>
      </div>

      <CardSection title="Devices" bodyClassName="p-0 sm:p-0">
        {devices.isLoading ? (
          <div className="p-5">
            <Skeleton className="h-24 w-full" />
          </div>
        ) : !devices.data?.devices.length ? (
          <Muted>No devices yet. Click “Add device” to set one up.</Muted>
        ) : (
          <DeviceTable
            devices={devices.data.devices.map((d) => ({ status: d, metric: metricByUuid.get(d.device_uuid) }))}
            latestBuild={devices.data.latest?.build ?? 0}
            onRevoke={revoke}
            onDelete={remove_}
          />
        )}
      </CardSection>
    </div>
  );
}

function DeviceTable({
  devices,
  latestBuild,
  onRevoke,
  onDelete,
}: {
  devices: { status: import("../lib/api").DeviceStatus; metric?: DeviceSummary }[];
  latestBuild: number;
  onRevoke: (uuid: string) => void;
  onDelete: (uuid: string, label: string) => void;
}) {
  return (
    <div className="divide-y">
      {devices.map(({ status, metric }) => (
        <div key={status.device_uuid} className="flex items-center gap-3 px-4 py-3 sm:px-5">
          <div className="min-w-0 flex-1">
            <Link to={`/devices/${status.device_uuid}`} className="flex items-center gap-2">
              <span className="truncate font-medium hover:text-primary hover:underline">
                {status.name || status.hostname || status.device_uuid.slice(0, 8)}
              </span>
              <StatusBadge device={status} />
            </Link>
            <div className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
              <span className="truncate">
                {status.agent_version ? `app v${status.agent_version} · ` : ""}
                {ago(status.last_seen)}
              </span>
              {latestBuild > 0 && status.agent_build > 0 && status.agent_build < latestBuild && (
                <span
                  className="shrink-0 rounded bg-amber-500/15 px-1.5 py-0.5 text-[10px] font-medium text-amber-600 dark:text-amber-400"
                  title="The FabScreenTime app on this computer is behind the latest version. It should update itself within a few minutes."
                >
                  update pending
                </span>
              )}
              {status.clock_skew_known && Math.abs(status.clock_skew) >= CLOCK_SKEW_FLAG_SECONDS && (
                <span
                  className="shrink-0 rounded bg-rose-500/15 px-1.5 py-0.5 text-[10px] font-medium text-rose-600 dark:text-rose-400"
                  title={`This computer's clock is ${fmtClockSkew(status.clock_skew)} compared with the server. When a clock is off by that much, its activity gets logged at the wrong times — check the computer's time settings.`}
                >
                  clock {fmtClockSkew(status.clock_skew)}
                </span>
              )}
            </div>
          </div>
          <div className="tnum hidden text-right text-sm sm:block">
            {fmtMinutes(metric?.monitor_minutes ?? 0)}
          </div>
          {status.status === "revoked" ? (
            <span className="text-xs text-muted-foreground">revoked</span>
          ) : (
            <Button variant="danger" size="sm" onClick={() => onRevoke(status.device_uuid)}>
              Revoke
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon"
            className="text-muted-foreground hover:text-danger"
            aria-label="Delete device"
            title="Delete device and its data"
            onClick={() =>
              onDelete(
                status.device_uuid,
                status.name || status.hostname || status.device_uuid.slice(0, 8),
              )
            }
          >
            <Trash2 className="size-4" />
          </Button>
          <Link to={`/devices/${status.device_uuid}`} className="text-muted-foreground hover:text-foreground">
            <ChevronRight className="size-4" />
          </Link>
        </div>
      ))}
    </div>
  );
}
