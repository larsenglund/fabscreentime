import { useState } from "react";
import { Link, Outlet } from "react-router-dom";
import { MonitorSmartphone, Plus } from "lucide-react";
import { Button } from "./ui/button";
import { ThemeToggle } from "./ThemeToggle";
import { AddDeviceDialog } from "./AddDeviceDialog";

export function Layout() {
  const [addOpen, setAddOpen] = useState(false);
  return (
    <div className="min-h-screen">
      <header className="sticky top-0 z-30 border-b bg-background/80 backdrop-blur">
        <div className="mx-auto flex max-w-5xl items-center justify-between gap-3 px-4 py-3 sm:px-6">
          <Link to="/" className="flex items-center gap-2">
            <MonitorSmartphone className="size-5 text-primary" />
            <span className="font-semibold tracking-tight">FabScreenTime</span>
          </Link>
          <div className="flex items-center gap-2">
            <Button size="sm" onClick={() => setAddOpen(true)}>
              <Plus className="size-4" /> <span className="hidden sm:inline">Add device</span>
            </Button>
            <ThemeToggle />
          </div>
        </div>
      </header>

      <main className="mx-auto max-w-5xl px-4 py-6 sm:px-6">
        <Outlet />
      </main>

      <footer className="mx-auto max-w-5xl px-4 pb-10 pt-2 text-xs text-muted-foreground sm:px-6">
        “Screentime” means the time a screen is physically switched on. It's the best measure we
        have of when a device is really being used, though it isn't foolproof — a screen can be left
        on. The lighter shade shows when someone was actually at the keyboard or mouse.
      </footer>

      <AddDeviceDialog open={addOpen} onClose={() => setAddOpen(false)} />
    </div>
  );
}
