import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ChevronLeft, Loader2, Search } from "lucide-react";
import { useClearTitles, useDevice, useTitles } from "../lib/api";
import { fmtMinutes, ago } from "../lib/format";
import { RangeSwitcher, type RangeKey } from "../components/RangeSwitcher";
import { CardSection } from "../components/ui/card";
import { Button } from "../components/ui/button";
import { Skeleton, Muted } from "../components/ui/skeleton";

const inputCls =
  "h-9 w-full rounded-lg border bg-background px-3 text-sm outline-none focus-visible:border-primary";

export function DeviceTitles() {
  const { uuid = "" } = useParams();
  const [range, setRange] = useState<RangeKey>("7d");
  const [exe, setExe] = useState("");
  const [search, setSearch] = useState("");
  const [q, setQ] = useState("");

  // Debounce the search box so we don't hit the server on every keystroke.
  useEffect(() => {
    const t = setTimeout(() => setQ(search.trim()), 300);
    return () => clearTimeout(t);
  }, [search]);

  const device = useDevice(uuid);
  const titles = useTitles(uuid, range, exe, q);
  const d = device.data;
  const rows = titles.data?.titles ?? [];
  const apps = titles.data?.apps ?? [];
  const filtering = !!exe || !!q;

  // Password-gated "wipe all logged titles" (a light guard, checked server-side).
  const clear = useClearTitles(uuid);
  const [confirming, setConfirming] = useState(false);
  const [pw, setPw] = useState("");
  function submitClear() {
    if (clear.isPending || !pw) return;
    clear.mutate(pw, {
      onSuccess: () => {
        setConfirming(false);
        setPw("");
      },
    });
  }
  function cancelClear() {
    setConfirming(false);
    setPw("");
    clear.reset();
  }

  return (
    <div className="space-y-6">
      <div>
        <Link
          to={`/devices/${uuid}`}
          className="mb-3 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        >
          <ChevronLeft className="size-4" /> {d ? d.name || d.hostname || uuid.slice(0, 8) : "Device"}
        </Link>
        <h1 className="text-xl font-semibold tracking-tight">Window titles</h1>
        <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
          The title of the foreground window each minute the screen was on — page titles in a
          browser, open document and file names, and so on. Times are how long each was in front.
        </p>
      </div>

      {d && d.log_titles === false && (
        <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-amber-700 dark:text-amber-300">
          Window-title logging is currently <strong>off</strong> for this computer, so nothing new is
          being recorded (only the app and activity are). Anything below is from before it was turned
          off. You can turn it back on under Privacy on the{" "}
          <Link to={`/devices/${uuid}`} className="underline">
            device page
          </Link>
          .
        </div>
      )}

      <CardSection
        title="Titles"
        action={<RangeSwitcher value={range} onChange={setRange} />}
        bodyClassName="p-0 sm:p-0"
      >
        <div className="flex flex-col gap-2 border-b p-4 sm:flex-row sm:items-center sm:px-5">
          <div className="relative flex-1">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search titles (e.g. youtube, invoice, github)"
              aria-label="Search window titles"
              className={inputCls + " pl-9"}
            />
          </div>
          <select
            value={exe}
            onChange={(e) => setExe(e.target.value)}
            aria-label="Filter by app"
            className={inputCls + " sm:w-52"}
          >
            <option value="">All apps</option>
            {apps.map((a) => (
              <option key={a} value={a}>
                {a}
              </option>
            ))}
          </select>
        </div>

        {titles.isLoading ? (
          <div className="p-5">
            <Skeleton className="h-40 w-full" />
          </div>
        ) : rows.length === 0 ? (
          <Muted>
            {filtering
              ? "No window titles match those filters in this range."
              : "No window titles recorded in this range."}
          </Muted>
        ) : (
          <>
            <ul className="divide-y">
              {rows.map((t, i) => (
                <li key={i} className="flex items-baseline justify-between gap-4 px-4 py-2.5 sm:px-5">
                  <div className="min-w-0">
                    {/* Rendered as text by React — a hostile title cannot inject markup. */}
                    <div className="break-words text-sm" title={t.title}>
                      {t.title}
                    </div>
                    <div className="mt-0.5 truncate text-xs text-muted-foreground">
                      {t.exe} · last {ago(t.last_seen)}
                    </div>
                  </div>
                  <div className="tnum shrink-0 text-sm text-muted-foreground">{fmtMinutes(t.minutes)}</div>
                </li>
              ))}
            </ul>
            <div className="px-4 py-2.5 text-xs text-muted-foreground sm:px-5">
              {rows.length} {rows.length === 1 ? "title" : "titles"}
              {rows.length >= 200 ? " (showing the top 200)" : ""}
            </div>
          </>
        )}
      </CardSection>

      <CardSection title="Clear logged titles">
        <p className="max-w-2xl text-sm text-muted-foreground">
          Permanently erase every window title recorded for this computer, across all time.
          Screentime, top apps, and everything else are kept — only the titles are removed. This
          can't be undone.
        </p>
        {!confirming ? (
          <Button
            variant="danger"
            size="sm"
            className="mt-3"
            onClick={() => {
              clear.reset();
              setConfirming(true);
            }}
          >
            Clear all logged titles…
          </Button>
        ) : (
          <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-center">
            <input
              type="password"
              autoFocus
              value={pw}
              onChange={(e) => setPw(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") submitClear();
                if (e.key === "Escape") cancelClear();
              }}
              placeholder="Password"
              aria-label="Password to clear titles"
              className={inputCls + " sm:w-48"}
            />
            <div className="flex gap-2">
              <Button variant="danger" size="sm" onClick={submitClear} disabled={clear.isPending || !pw}>
                {clear.isPending && <Loader2 className="size-4 animate-spin" />}
                Erase titles
              </Button>
              <Button variant="ghost" size="sm" onClick={cancelClear} disabled={clear.isPending}>
                Cancel
              </Button>
            </div>
          </div>
        )}
        {clear.isError && (
          <p className="mt-2 text-xs text-danger">
            {(clear.error as Error)?.message === "wrong-password"
              ? "Wrong password."
              : "Couldn't clear titles. Try again."}
          </p>
        )}
        {clear.isSuccess && !confirming && (
          <p className="mt-2 text-xs text-muted-foreground">
            Cleared {clear.data?.cleared ?? 0} logged {clear.data?.cleared === 1 ? "title" : "titles"}.
          </p>
        )}
      </CardSection>
    </div>
  );
}
