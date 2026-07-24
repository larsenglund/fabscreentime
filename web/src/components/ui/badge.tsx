import type { HTMLAttributes } from "react";
import { cn } from "../../lib/cn";
import type { DeviceStatus } from "../../lib/api";
import { online } from "../../lib/format";

export function Badge({ className, ...props }: HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] font-medium",
        className,
      )}
      {...props}
    />
  );
}

const dotColor: Record<string, string> = {
  online: "bg-monitor",
  offline: "bg-muted-foreground",
  pending: "bg-warn",
  expired: "bg-muted-foreground",
  revoked: "bg-danger",
};

/** deviceState collapses status + last-seen into a single presented state. */
export function deviceState(d: Pick<DeviceStatus, "status" | "last_seen">): string {
  if (d.status === "active") return online(d.last_seen) ? "online" : "offline";
  return d.status;
}

/** StatusBadge shows a colored dot + label for a device's presented state. */
export function StatusBadge({ device }: { device: Pick<DeviceStatus, "status" | "last_seen"> }) {
  const state = deviceState(device);
  return (
    <Badge className="text-muted-foreground">
      <span className={cn("size-1.5 rounded-full", dotColor[state] ?? "bg-muted-foreground")} />
      {state}
    </Badge>
  );
}
