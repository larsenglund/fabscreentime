import { useState } from "react";
import { Moon, Sun } from "lucide-react";
import { Button } from "./ui/button";

/** ThemeToggle flips the .dark class on <html> and persists the choice. The
 *  initial class is applied pre-paint in index.html, so this only mirrors it. */
export function ThemeToggle() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"));

  function toggle() {
    const next = !dark;
    document.documentElement.classList.toggle("dark", next);
    try {
      localStorage.setItem("fst-theme", next ? "dark" : "light");
    } catch {
      /* ignore private-mode storage errors */
    }
    setDark(next);
  }

  return (
    <Button variant="ghost" size="icon" onClick={toggle} aria-label="Toggle theme">
      {dark ? <Sun className="size-4" /> : <Moon className="size-4" />}
    </Button>
  );
}
