import { cn } from "../lib/cn";

export const RANGES = [
  { k: "24h", l: "Today" },
  { k: "7d", l: "7 days" },
  { k: "30d", l: "30 days" },
] as const;

export type RangeKey = (typeof RANGES)[number]["k"];

/** RangeSwitcher is a segmented control driving the page's time window. */
export function RangeSwitcher({ value, onChange }: { value: string; onChange: (k: RangeKey) => void }) {
  return (
    <div className="inline-flex rounded-lg border bg-card p-0.5">
      {RANGES.map((r) => (
        <button
          key={r.k}
          onClick={() => onChange(r.k)}
          className={cn(
            "rounded-md px-3 py-1 text-[13px] font-medium transition-colors",
            value === r.k
              ? "bg-primary text-primary-foreground"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {r.l}
        </button>
      ))}
    </div>
  );
}
