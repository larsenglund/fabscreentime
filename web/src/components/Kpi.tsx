import type { ReactNode } from "react";

/** Kpi is a single stat tile: a small label, a large tabular figure, and an
 *  optional sub-line for deltas/context. */
export function Kpi({ label, value, sub }: { label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <div className="rounded-xl border bg-card p-4">
      <div className="text-xs font-medium text-muted-foreground">{label}</div>
      <div className="mt-1 text-2xl font-semibold tracking-tight tnum">{value}</div>
      {sub != null && <div className="mt-0.5 text-xs text-muted-foreground">{sub}</div>}
    </div>
  );
}
