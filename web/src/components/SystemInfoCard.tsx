import type { ReactNode } from "react";
import {
  AppWindow,
  CircuitBoard,
  Cpu,
  EthernetPort,
  HardDrive,
  Laptop,
  MemoryStick,
  MonitorCog,
  Network,
  TriangleAlert,
  Wifi,
  type LucideIcon,
} from "lucide-react";
import { useSystemInfo, type DriveInfo, type MemoryModule, type SystemInfo } from "../lib/api";
import { ago, fmtDiskSize, fmtMemory } from "../lib/format";
import { CardSection } from "./ui/card";
import { Skeleton } from "./ui/skeleton";

/** SystemInfoCard shows a computer's hardware and network, as it last reported them. */
export function SystemInfoCard({ uuid }: { uuid: string }) {
  const q = useSystemInfo(uuid);
  const info = q.data?.system_info;
  const updated = q.data?.updated_at ?? 0;

  return (
    <CardSection
      title="Computer"
      action={
        updated > 0 && (
          <span
            className="text-xs text-muted-foreground"
            title="The computer re-checks its hardware every 30 minutes and sends it again when something changes."
          >
            updated {ago(updated)}
          </span>
        )
      }
    >
      {q.isLoading ? (
        <Skeleton className="h-56 w-full" />
      ) : !info ? (
        <p className="text-sm text-muted-foreground">
          Not reported yet. A computer sends its hardware details a couple of minutes after it starts
          with the latest version of the app — it updates itself, so this fills in on its own.
        </p>
      ) : (
        <SystemInfoRows info={info} />
      )}
    </CardSection>
  );
}

function SystemInfoRows({ info }: { info: SystemInfo }) {
  const { cpu, memory, gpus, board, drives, network, os } = info;
  const systemModel =
    board?.system_model && board.system_model !== board.product
      ? withMaker(board.system_maker, board.system_model)
      : "";
  const boardName = withMaker(board?.manufacturer, board?.product);
  const rows: ReactNode[] = [];

  if (systemModel) {
    rows.push(
      <Row key="model" icon={Laptop} label="Model">
        <Item main={systemModel} />
      </Row>,
    );
  }
  if (cpu?.model) {
    rows.push(
      <Row key="cpu" icon={Cpu} label="Processor">
        <Item
          main={cpu.model}
          details={[
            !mentions(cpu.model, cpu.vendor) && cpu.vendor,
            cpu.cores ? plural(cpu.cores, "core") : "",
            cpu.threads ? plural(cpu.threads, "thread") : "",
          ]}
        />
      </Row>,
    );
  }
  if (memory?.total_bytes) {
    const mods = memory.modules ?? [];
    const rated = Math.max(0, ...mods.map((m) => m.rated_mts ?? 0));
    rows.push(
      <Row key="memory" icon={MemoryStick} label="Memory">
        <Item
          main={[
            [fmtMemory(memory.total_bytes), memory.type].filter(Boolean).join(" "),
            memory.speed_mts ? `${memory.speed_mts} MT/s` : "",
          ]
            .filter(Boolean)
            .join(" · ")}
          details={[
            ...moduleGroups(mods),
            memory.slots && mods.length ? `${mods.length} of ${plural(memory.slots, "slot")} used` : "",
          ]}
          warnings={[
            // A common, fixable slowdown: XMP/EXPO not enabled in the BIOS.
            memory.speed_mts && rated > memory.speed_mts
              ? `Running below the sticks' rated ${rated} MT/s — XMP/EXPO is probably off in the BIOS`
              : "",
          ]}
        />
      </Row>,
    );
  }
  if (gpus?.length) {
    rows.push(
      <Row key="gpu" icon={MonitorCog} label="Graphics">
        {gpus.map((g, i) => {
          // With the card's own driver missing or broken, Windows falls back to
          // its basic one: the model is then unknown, but the chip maker isn't.
          const noDriver = /microsoft basic (display|render)/i.test(g.model);
          return (
            <Item
              key={i}
              main={noDriver ? `${g.vendor ?? "Unknown"} graphics card` : g.model}
              details={[
                !noDriver && !mentions(g.model, g.vendor) && g.vendor,
                g.vram_bytes ? `${fmtMemory(g.vram_bytes)} video memory` : "",
                g.maker ? `${g.maker} card` : "",
              ]}
              warnings={[
                noDriver && "No graphics driver installed — Windows is using its basic fallback driver",
              ]}
            />
          );
        })}
      </Row>,
    );
  }
  if (boardName) {
    rows.push(
      <Row key="board" icon={CircuitBoard} label="Motherboard">
        <Item main={boardName} details={[board?.bios ? `BIOS ${board.bios}` : ""]} />
      </Row>,
    );
  }
  if (drives?.length) {
    rows.push(
      <Row key="drives" icon={HardDrive} label={drives.length === 1 ? "Drive" : "Drives"}>
        {drives.map((d, i) => {
          const iface = driveInterface(d);
          return (
            <Item
              key={i}
              main={d.model}
              title={iface.hint}
              details={[
                fmtDiskSize(d.size_bytes ?? 0),
                iface.label,
                d.media,
                !mentions(d.model, d.maker) && d.maker,
              ]}
            />
          );
        })}
      </Row>,
    );
  }
  if (network && (network.ipv4 || network.name)) {
    rows.push(
      <Row
        key="network"
        icon={network.kind === "Wi-Fi" ? Wifi : network.kind === "Ethernet" ? EthernetPort : Network}
        label="Network"
      >
        <Item
          main={[network.name, network.ipv4].filter(Boolean).join(" · ")}
          details={[network.kind, network.link_mbps ? fmtLink(network.link_mbps) : "", network.adapter]}
          warnings={[slowWiredLink(network) && "A gigabit adapter connected at only 100 Mbps — usually a worn cable or a slow port"]}
        />
      </Row>,
    );
  }
  if (os) {
    rows.push(
      <Row key="os" icon={AppWindow} label="OS">
        <Item main={os} />
      </Row>,
    );
  }

  if (!rows.length) {
    return <p className="text-sm text-muted-foreground">This computer didn't report any hardware details.</p>;
  }
  return <dl className="divide-y">{rows}</dl>;
}

function Row({ icon: Icon, label, children }: { icon: LucideIcon; label: string; children: ReactNode }) {
  return (
    <div className="grid gap-1 py-3 first:pt-0 last:pb-0 sm:grid-cols-[8.5rem_minmax(0,1fr)] sm:gap-4">
      <dt className="flex items-center gap-2 text-sm text-muted-foreground sm:items-start sm:pt-px">
        <Icon className="size-4 shrink-0" aria-hidden="true" />
        {label}
      </dt>
      <dd className="min-w-0 space-y-2 text-sm">{children}</dd>
    </div>
  );
}

type Parts = (string | false | 0 | undefined | null)[];

/** Item is one thing in a row: a main line, a quieter line of details, and any
 *  warnings worth acting on. Empty parts are dropped. */
function Item({
  main,
  details = [],
  warnings = [],
  title,
}: {
  main: ReactNode;
  details?: Parts;
  warnings?: Parts;
  title?: string;
}) {
  const shown = details.filter((d): d is string => !!d);
  const warn = warnings.filter((w): w is string => !!w);
  return (
    <div title={title}>
      <div className="break-words font-medium">{main}</div>
      {shown.length > 0 && (
        <div className="mt-0.5 break-words text-xs text-muted-foreground">{shown.join(" · ")}</div>
      )}
      {warn.map((w) => (
        <div key={w} className="mt-1 flex items-start gap-1.5 text-xs text-amber-700 dark:text-amber-400">
          <TriangleAlert className="mt-px size-3.5 shrink-0" aria-hidden="true" />
          <span>{w}</span>
        </div>
      ))}
    </div>
  );
}

/** slowWiredLink spots a gigabit-or-faster Ethernet adapter that negotiated a
 *  slower link, which almost always means a damaged cable or a 100 Mbps port. */
function slowWiredLink(n: NonNullable<SystemInfo["network"]>): boolean {
  return (
    n.kind === "Ethernet" &&
    !!n.link_mbps &&
    n.link_mbps < 1000 &&
    /\b(gbe|gigabit|2\.5\s?g|5\s?g|10\s?g)/i.test(n.adapter ?? "")
  );
}

/** mentions reports whether text already names name, so it isn't repeated. */
function mentions(text?: string, name?: string): boolean {
  return !!text && !!name && text.toLowerCase().includes(name.toLowerCase());
}

/** withMaker prefixes a model with its maker unless the model already says it. */
function withMaker(maker?: string, model?: string): string {
  if (!model) return maker ?? "";
  return maker && !mentions(model, maker) ? `${maker} ${model}` : model;
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

/** moduleGroups summarizes identical RAM sticks: "2 × 16 GB G.Skill F4-3200C16-16GIS". */
function moduleGroups(mods: MemoryModule[]): string[] {
  const groups = new Map<string, { n: number; m: MemoryModule }>();
  for (const m of mods) {
    const key = `${m.size_bytes}|${m.manufacturer ?? ""}|${m.part_number ?? ""}`;
    const g = groups.get(key);
    if (g) g.n++;
    else groups.set(key, { n: 1, m });
  }
  return [...groups.values()].map(({ n, m }) =>
    [`${n} × ${fmtMemory(m.size_bytes)}`, m.manufacturer, m.part_number].filter(Boolean).join(" "),
  );
}

/** driveInterface labels how a drive is connected. Windows reports the bus, not
 *  the slot, so "M.2" is only claimed where it's certain or the model says so. */
function driveInterface(d: DriveInfo): { label?: string; hint?: string } {
  if (d.bus === "NVMe") {
    return { label: "M.2 NVMe", hint: "NVMe drives sit in an M.2 slot (or, rarely, on a PCIe card)." };
  }
  if (d.bus === "SATA") {
    const m2 = /\bM\.?2\b/i.test(d.model);
    return {
      label: m2 ? "M.2 SATA" : "SATA",
      hint: m2
        ? undefined
        : "Connected over SATA. Windows can't tell a regular 2.5″/3.5″ SATA drive from an M.2 SATA one, so M.2 is only shown when the model name says so.",
    };
  }
  return { label: d.bus };
}

function fmtLink(mbps: number): string {
  return mbps >= 1000 ? `${+(mbps / 1000).toFixed(1)} Gbps link` : `${mbps} Mbps link`;
}
