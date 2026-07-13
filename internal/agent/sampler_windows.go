//go:build windows

package agent

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Real Win32 sampler: foreground window (title + owning exe) and input idle.
// Monitor on/off is intentionally NOT sampled here in Phase 0 — it needs a
// message pump and per-device connection/power logic, which lands in Phase 2
// (PLAN.md §4.4). The cmd/montest probe validates the monitor signal first.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procGetLastInputInfo         = user32.NewProc("GetLastInputInfo")
	procGetTickCount             = kernel32.NewProc("GetTickCount")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

type winSampler struct{}

// NewSampler returns the real Win32 sampler on Windows.
func NewSampler() Sampler { return &winSampler{} }

func (s *winSampler) Sample() (Reading, error) {
	idle := idleMS()
	exe, title := foreground()
	return Reading{
		MonitorsActive: -1, // not sampled in Phase 0
		DisplayPower:   -1, // not sampled in Phase 0
		IsIdle:         idle > IdleThresholdMS,
		IdleMS:         idle,
		ExeName:        exe,
		WindowTitle:    title,
	}, nil
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
