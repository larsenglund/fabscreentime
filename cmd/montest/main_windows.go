//go:build windows

// Command montest is the Phase 0 signal-validation probe (PLAN.md §0.1). It logs
// the active-monitor count and input-idle once a second to a CSV so you can
// answer the one gating question for this project:
//
//	When you physically power the monitor off the way the household does,
//	does monitors_active drop to 0?
//
// If yes → connection-presence is a clean, un-fakeable "screen off" signal
// (DisplayPort usually does this). If it stays put → that machine is HDMI-style
// and needs the fallback ladder (PLAN.md §0.1 / §4.4c). Run it on at least one
// DisplayPort and one HDMI machine, power the monitor off for ~30s, then on.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procGetLastInputInfo    = user32.NewProc("GetLastInputInfo")
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procGetTickCount        = kernel32.NewProc("GetTickCount")
)

const smCMonitors = 80 // SM_CMONITORS: number of display monitors on the desktop

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

func monitorCount() int {
	n, _, _ := procGetSystemMetrics.Call(uintptr(smCMonitors))
	return int(n)
}

func idleMS() int64 {
	lii := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&lii)))
	if r == 0 {
		return 0
	}
	tick, _, _ := procGetTickCount.Call()
	return int64(uint32(tick) - lii.dwTime)
}

func foregroundPresent() int {
	h, _, _ := procGetForegroundWindow.Call()
	if h == 0 {
		return 0 // locked / secure desktop
	}
	return 1
}

func sampleLine() string {
	return fmt.Sprintf("%s,%d,%d,%d",
		time.Now().Format(time.RFC3339), monitorCount(), idleMS(), foregroundPresent())
}

const header = "timestamp,monitors_active,idle_ms,foreground_present"

func main() {
	out := flag.String("out", "montest.csv", "CSV output path")
	interval := flag.Duration("interval", time.Second, "sample interval")
	once := flag.Bool("once", false, "print one reading and exit (CI smoke test)")
	flag.Parse()

	if *once {
		fmt.Println(header)
		fmt.Println(sampleLine())
		return
	}

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create:", err)
		os.Exit(1)
	}
	defer f.Close()

	fmt.Fprintln(f, header)
	fmt.Println(header)
	fmt.Printf("Logging every %s to %s.\n", *interval, *out)
	fmt.Println("Now physically power your monitor OFF for ~30s, then back ON. Ctrl+C to stop.")

	t := time.NewTicker(*interval)
	defer t.Stop()
	for range t.C {
		line := sampleLine()
		fmt.Fprintln(f, line)
		_ = f.Sync()
		fmt.Println(line)
	}
}
