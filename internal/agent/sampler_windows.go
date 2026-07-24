//go:build windows

package agent

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Real Win32 sampler: foreground window (title + owning exe), input idle, and
// the monitor signals. Hardware validation (MONTEST-RESULTS.md) showed that on
// this fleet a physically powered-off monitor NEVER drops out of the display
// topology — the connected-display count (SM_CMONITORS) stays put on both DP
// and HDMI — so the count is kept as a raw signal but the load-bearing off
// detector is the DDC/CI watcher (ddc_windows.go): DP power-off removes the
// physical-monitor handle; HDMI power-off answers VCP 0xD6 with standby/off.
// Display *power* state (GUID_SESSION_DISPLAY_STATUS) needs a message pump and
// is still deferred; DisplayPower stays -1 (unknown) meanwhile.

const smCMonitors = 80 // SM_CMONITORS

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procGetLastInputInfo         = user32.NewProc("GetLastInputInfo")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	procGetTickCount             = kernel32.NewProc("GetTickCount")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

type winSampler struct {
	ddc *ddcWatcher
}

// NewSampler returns the real Win32 sampler on Windows.
func NewSampler() Sampler { return &winSampler{ddc: newDDCWatcher()} }

func (s *winSampler) Sample() (Reading, error) {
	idle := idleMS()
	exe, title := foreground()
	ddcPower, ddcArmed := s.ddc.read()
	return Reading{
		MonitorsActive: monitorCount(),
		DisplayPower:   -1, // display power (DPMS) via message pump: deferred, see file header
		DDCPower:       ddcPower,
		DDCArmed:       ddcArmed,
		IsIdle:         idle > IdleThresholdMS,
		IdleMS:         idle,
		ExeName:        exe,
		WindowTitle:    title,
	}, nil
}

// MonitorOn reports the composite monitor state for the transition poller,
// applying the same rule as deriveMonitorOn: any off testimony wins.
func (s *winSampler) MonitorOn() int {
	ddcPower, ddcArmed := s.ddc.read()
	if monitorCount() > 0 && !ddcSaysOff(ddcPower, ddcArmed) {
		return 1
	}
	return 0
}

// monitorCount returns the number of display monitors on the desktop. It drops
// when a monitor is disconnected (including a DisplayPort monitor powered off).
func monitorCount() int {
	n, _, _ := procGetSystemMetrics.Call(uintptr(smCMonitors))
	return int(n)
}

// idleMS returns milliseconds since the last mouse/keyboard input.
func idleMS() int64 {
	lii := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&lii)))
	if r == 0 {
		return 0
	}
	tick, _, _ := procGetTickCount.Call()
	// 32-bit unsigned subtraction is correct across the ~49.7-day GetTickCount
	// wrap; mixing in a 64-bit "now" would corrupt the delta (PLAN.md §4.3).
	delta := uint32(tick) - lii.dwTime
	return int64(delta)
}

// foreground returns the active window's owning exe basename and its title.
// Either may be empty: a NULL foreground means locked/secure desktop, and an
// access-denied exe (elevated/protected process) falls back to the title.
func foreground() (exe, title string) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return "", ""
	}
	title = windowText(hwnd)
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != 0 {
		exe = processExe(pid)
	}
	return exe, title
}

func windowText(hwnd uintptr) string {
	buf := make([]uint16, 512)
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

// processExe returns the process image basename, or "" on access-denied so the
// caller can fall back to the window title (PLAN.md §4.2).
func processExe(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	full := windows.UTF16ToString(buf[:size])
	if i := strings.LastIndexAny(full, `\/`); i >= 0 {
		return full[i+1:]
	}
	return full
}
