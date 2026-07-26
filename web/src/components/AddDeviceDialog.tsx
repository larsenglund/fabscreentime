import { useEffect, useState } from "react";
import { Check, Download, Loader2, X } from "lucide-react";
import { buildBatInstaller, manifestSHA256, usePrepareEnroll, type DeviceStatus } from "../lib/api";
import { Button } from "./ui/button";

export function AddDeviceDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [name, setName] = useState("");
  const [secret, setSecret] = useState<string | null>(null);
  const [sha, setSha] = useState("");
  const [uuid, setUuid] = useState<string | null>(null);
  const [connected, setConnected] = useState(false);
  const prepare = usePrepareEnroll();

  // Reset when the dialog is (re)opened.
  useEffect(() => {
    if (open) {
      setName("");
      setSecret(null);
      setSha("");
      setUuid(null);
      setConnected(false);
      prepare.reset();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  // Poll the pending device until it first checks in.
  useEffect(() => {
    if (!uuid || connected) return;
    const id = setInterval(async () => {
      try {
        const d: DeviceStatus = await fetch(`/api/devices/${uuid}`).then((r) => r.json());
        if (d.status === "active" && d.last_seen) setConnected(true);
      } catch {
        /* keep polling */
      }
    }, 3000);
    return () => clearInterval(id);
  }, [uuid, connected]);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    if (open) window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open) return null;

  function download(secretValue: string, shaValue: string) {
    const bat = buildBatInstaller(window.location.origin, secretValue, shaValue);
    const url = URL.createObjectURL(new Blob([bat], { type: "application/octet-stream" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "install-fabscreentime.bat";
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  async function generate() {
    const res = await prepare.mutateAsync(name.trim());
    const shaValue = await manifestSHA256();
    setSecret(res.enroll_secret);
    setSha(shaValue);
    setUuid(res.device_uuid);
    setConnected(false);
    download(res.enroll_secret, shaValue);
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-4 pt-[8vh] backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className="w-full max-w-lg rounded-2xl border bg-card shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b px-5 py-3.5">
          <h2 className="font-semibold">Add a device</h2>
          <Button variant="ghost" size="icon" onClick={onClose} aria-label="Close">
            <X className="size-4" />
          </Button>
        </div>

        <div className="space-y-5 p-5">
          <Step n={1} title="Name the machine">
            <div className="flex gap-2">
              <input
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && !prepare.isPending && generate()}
                placeholder="e.g. Living room PC"
                className="h-9 w-full rounded-lg border bg-background px-3 text-sm outline-none focus-visible:border-primary"
              />
              <Button onClick={generate} disabled={prepare.isPending} className="shrink-0">
                {prepare.isPending ? <Loader2 className="size-4 animate-spin" /> : null}
                Generate installer
              </Button>
            </div>
            {prepare.isError && (
              <p className="mt-2 text-xs text-danger">Couldn't create an installer. Try again.</p>
            )}
          </Step>

          {secret && (
            <>
              <Step n={2} title="Run the installer on that PC">
                <p className="mb-3 text-sm text-muted-foreground">
                  The installer just downloaded. Put{" "}
                  <code className="rounded bg-muted px-1 py-0.5 text-[12px]">install-fabscreentime.bat</code>{" "}
                  on the new PC and <strong>double-click it</strong> — no terminal, nothing to type.
                </p>
                <Button onClick={() => download(secret, sha)}>
                  <Download className="size-4" /> Download installer
                </Button>
                <ul className="mt-3 space-y-1 text-xs text-muted-foreground">
                  <li>
                    • If the browser or Windows warns about the file, choose <strong>Keep</strong> /{" "}
                    <strong>Run anyway</strong> — it's your own server.
                  </li>
                  <li>• Installs quietly in the background, starts when you log in, and keeps itself up to date.</li>
                  <li>• Run it within ~15 minutes; the one-time key expires.</li>
                </ul>
              </Step>

              <Step n={3} title="Wait for first check-in">
                {connected ? (
                  <p className="flex items-center gap-2 font-medium text-monitor">
                    <Check className="size-4" /> Connected — the device is reporting.
                  </p>
                ) : (
                  <p className="flex items-center gap-2 text-sm text-muted-foreground">
                    <Loader2 className="size-4 animate-spin" /> Waiting for the device to report…
                  </p>
                )}
              </Step>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-2 flex items-center gap-2 text-xs font-semibold text-muted-foreground">
        <span className="flex size-5 items-center justify-center rounded-full bg-primary text-[11px] text-primary-foreground">
          {n}
        </span>
        {title}
      </div>
      {children}
    </div>
  );
}
