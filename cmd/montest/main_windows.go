//go:build windows

// Command montest is the Phase 0 signal-validation probe (PLAN.md §0.1). It logs
// every candidate "is the monitor physically on?" signal once a second to a CSV
// so you can answer the one gating question for this project:
//
//	When you physically power the monitor off the way the household does,
//	which signal (if any) changes?
//
// Columns, in falling order on the §0.1 / §4.4c ladder:
//
//	monitors_active  — GetSystemMetrics(SM_CMONITORS): monitors on the desktop.
//	qdc_paths        — QueryDisplayConfig(QDC_ONLY_ACTIVE_PATHS): active display
//	                   paths in the topology (the §4.4a primary-signal candidate).
//	target_available — the first path's targetAvailable flag (-1 if no path).
//	ddc_power        — DDC/CI VCP 0xD6 power-mode reply from the first physical
//	                   monitor: 1=on, 2/3=standby/suspend, 4=off, 5=hard off;
//	                   -1 = no physical monitor handle, -2 = DDC query failed
//	                   (for many monitors a physically-off screen stops answering
//	                   DDC, so a flip to -2 IS the off-signal), -3 = not sampled
//	                   yet. Sampled every ~2s on its own goroutine because DDC
//	                   is slow; the cached value is logged each CSV row.
//	idle_ms          — GetLastInputInfo age (a climbing value marks the
//	                   hands-off window even when nothing else changes).
//
// If a count drops while the monitor is physically off → connection presence
// is a clean, un-fakeable "screen off" signal on that machine (typical consumer
// DisplayPort behaviour → monitor_detect_mode = connection). If the counts hold
// but ddc_power flips to -2/off → use the DDC rung (mode = ddc). If nothing
// changes → that machine is heuristic-mode (PLAN.md §4.4c). Run it on at least
// one DisplayPort and one HDMI machine: power the monitor off for ~30s, then
// back on, then Ctrl+C.
//
// At startup, and whenever the path count changes, it prints the connector
// type (DisplayPort/HDMI/…) and monitor name of every active path, so each
// run also records which connector the machine is actually on.
package main

import (
	"flag"
	"fmt"
	"os"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	dxva2    = windows.NewLazySystemDLL("dxva2.dll")

	procGetSystemMetrics            = user32.NewProc("GetSystemMetrics")
	procGetLastInputInfo            = user32.NewProc("GetLastInputInfo")
	procGetForegroundWindow         = user32.NewProc("GetForegroundWindow")
	procGetDisplayConfigBufferSizes = user32.NewProc("GetDisplayConfigBufferSizes")
	procQueryDisplayConfig          = user32.NewProc("QueryDisplayConfig")
	procDisplayConfigGetDeviceInfo  = user32.NewProc("DisplayConfigGetDeviceInfo")
	procEnumDisplayMonitors         = user32.NewProc("EnumDisplayMonitors")
	procGetTickCount                = kernel32.NewProc("GetTickCount")

	procGetNumberOfPhysicalMonitors     = dxva2.NewProc("GetNumberOfPhysicalMonitorsFromHMONITOR")
	procGetPhysicalMonitorsFromHMONITOR = dxva2.NewProc("GetPhysicalMonitorsFromHMONITOR")
	procDestroyPhysicalMonitor          = dxva2.NewProc("DestroyPhysicalMonitor")
	procGetVCPFeatureAndVCPFeatureReply = dxva2.NewProc("GetVCPFeatureAndVCPFeatureReply")
)

const (
	smCMonitors = 80 // SM_CMONITORS: number of display monitors on the desktop

	qdcOnlyActivePaths      = 0x2 // QDC_ONLY_ACTIVE_PATHS
	errorInsufficientBuffer = 122 // ERROR_INSUFFICIENT_BUFFER

	displayConfigDeviceInfoGetTargetName = 2 // DISPLAYCONFIG_DEVICE_INFO_GET_TARGET_NAME

	vcpPowerMode = 0xD6 // DDC/CI VCP code: display power mode
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

type luid struct {
	LowPart  uint32
	HighPart int32
}

type displayConfigPathSourceInfo struct {
	AdapterID   luid
	ID          uint32
	ModeInfoIdx uint32
	StatusFlags uint32
}

type displayConfigRational struct {
	Numerator   uint32
	Denominator uint32
}

type displayConfigPathTargetInfo struct {
	AdapterID        luid
	ID               uint32
	ModeInfoIdx      uint32
	OutputTechnology uint32
	Rotation         uint32
	Scaling          uint32
	RefreshRate      displayConfigRational
	ScanLineOrdering uint32
	TargetAvailable  int32
	StatusFlags      uint32
}

type displayConfigPathInfo struct {
	SourceInfo displayConfigPathSourceInfo
	TargetInfo displayConfigPathTargetInfo
	Flags      uint32
}

// displayConfigModeInfo only needs the right size/stride; the union payload
// (48 bytes, 8-aligned via the leading fields) is never read.
type displayConfigModeInfo struct {
	InfoType  uint32
	ID        uint32
	AdapterID luid
	data      [48]byte
}

type displayConfigTargetDeviceName struct {
	Header struct {
		Type      uint32
		Size      uint32
		AdapterID luid
		ID        uint32
	}
	Flags             uint32
	OutputTechnology  uint32
	EDIDManufactureID uint16
	EDIDProductCodeID uint16
	ConnectorInstance uint32
	FriendlyName      [64]uint16
	DevicePath        [128]uint16
}

// physicalMonitor is Win32 PHYSICAL_MONITOR (dxva2 low-level monitor API).
type physicalMonitor struct {
	Handle windows.Handle
	Desc   [128]uint16
}

func init() {
	// These structs are passed to Win32 by pointer; a size drift would corrupt.
	if unsafe.Sizeof(displayConfigPathInfo{}) != 72 ||
		unsafe.Sizeof(displayConfigModeInfo{}) != 64 ||
		unsafe.Sizeof(displayConfigTargetDeviceName{}) != 420 ||
		unsafe.Sizeof(physicalMonitor{}) != 264 {
		panic("win32 struct size mismatch")
	}
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

// activePaths returns the active display paths, retrying if the topology
// changes between sizing and querying (exactly what happens mid-experiment).
func activePaths() ([]displayConfigPathInfo, error) {
	for attempt := 0; attempt < 3; attempt++ {
		var nPath, nMode uint32
		r, _, _ := procGetDisplayConfigBufferSizes.Call(qdcOnlyActivePaths,
			uintptr(unsafe.Pointer(&nPath)), uintptr(unsafe.Pointer(&nMode)))
		if r != 0 {
			return nil, fmt.Errorf("GetDisplayConfigBufferSizes: error %d", r)
		}
		paths := make([]displayConfigPathInfo, nPath+1) // +1: keep a valid pointer when count is 0
		modes := make([]displayConfigModeInfo, nMode+1)
		r, _, _ = procQueryDisplayConfig.Call(qdcOnlyActivePaths,
			uintptr(unsafe.Pointer(&nPath)), uintptr(unsafe.Pointer(&paths[0])),
			uintptr(unsafe.Pointer(&nMode)), uintptr(unsafe.Pointer(&modes[0])), 0)
		if r == errorInsufficientBuffer {
			continue
		}
		if r != 0 {
			return nil, fmt.Errorf("QueryDisplayConfig: error %d", r)
		}
		return paths[:nPath], nil
	}
	return nil, fmt.Errorf("QueryDisplayConfig: topology kept changing")
}

func connectorName(t uint32) string {
	switch t {
	case 0:
		return "VGA"
	case 1:
		return "S-Video"
	case 2:
		return "Composite"
	case 3:
		return "Component"
	case 4:
		return "DVI"
	case 5:
		return "HDMI"
	case 6:
		return "LVDS (internal)"
	case 8:
		return "D_JPN"
	case 9:
		return "SDI"
	case 10:
		return "DisplayPort (external)"
	case 11:
		return "DisplayPort (embedded)"
	case 12:
		return "UDI (external)"
	case 13:
		return "UDI (embedded)"
	case 14:
		return "SDTV dongle"
	case 15:
		return "Miracast"
	case 16:
		return "Indirect (wired)"
	case 17:
		return "Indirect (virtual)"
	case 18:
		return "DisplayPort (USB tunnel)"
	case 0x80000000:
		return "Internal panel"
	case 0xFFFFFFFF:
		return "Other"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}

// targetName returns the monitor's EDID friendly name for one path, or "".
func targetName(adapter luid, id uint32) string {
	var tdn displayConfigTargetDeviceName
	tdn.Header.Type = displayConfigDeviceInfoGetTargetName
	tdn.Header.Size = uint32(unsafe.Sizeof(tdn))
	tdn.Header.AdapterID = adapter
	tdn.Header.ID = id
	r, _, _ := procDisplayConfigGetDeviceInfo.Call(uintptr(unsafe.Pointer(&tdn)))
	if r != 0 {
		return ""
	}
	return windows.UTF16ToString(tdn.FriendlyName[:])
}

// describePaths renders the current topology, one line per active path.
func describePaths() string {
	paths, err := activePaths()
	if err != nil {
		return fmt.Sprintf("active display paths: %v", err)
	}
	s := fmt.Sprintf("active display paths: %d", len(paths))
	for i, p := range paths {
		name := targetName(p.TargetInfo.AdapterID, p.TargetInfo.ID)
		if name == "" {
			name = "(no EDID name)"
		}
		s += fmt.Sprintf("\n  [%d] %s — %s (avail=%d flags=0x%x)", i,
			connectorName(p.TargetInfo.OutputTechnology), name,
			p.TargetInfo.TargetAvailable, p.TargetInfo.StatusFlags)
	}
	return s
}

// --- DDC/CI power probe (VCP 0xD6), the §4.4c "ddc" rung ---------------------

// ddcPower caches the latest VCP 0xD6 result; see the doc comment for values.
var ddcPower atomic.Int32

// enumResult/enumCallback: NewCallback allocations are permanent, so the
// callback is created exactly once; enumMonitors is only called from the DDC
// goroutine (or synchronously before it starts), never concurrently.
var enumResult []uintptr
var enumCallback = windows.NewCallback(func(hmon, hdc, rect, lparam uintptr) uintptr {
	enumResult = append(enumResult, hmon)
	return 1 // continue enumeration
})

func enumMonitors() []uintptr {
	enumResult = enumResult[:0]
	procEnumDisplayMonitors.Call(0, 0, enumCallback, 0)
	return enumResult
}

// queryDDCPower re-enumerates physical monitors (handles go stale across
// power events) and asks the first one for its VCP 0xD6 power mode.
func queryDDCPower() int32 {
	for _, hm := range enumMonitors() {
		var n uint32
		r, _, _ := procGetNumberOfPhysicalMonitors.Call(hm, uintptr(unsafe.Pointer(&n)))
		if r == 0 || n == 0 {
			continue
		}
		phys := make([]physicalMonitor, n)
		r, _, _ = procGetPhysicalMonitorsFromHMONITOR.Call(hm, uintptr(n), uintptr(unsafe.Pointer(&phys[0])))
		if r == 0 {
			continue
		}
		val := int32(-2) // have a physical handle, but the DDC query failed
		var pvct, cur, max uint32
		rr, _, _ := procGetVCPFeatureAndVCPFeatureReply.Call(uintptr(phys[0].Handle), vcpPowerMode,
			uintptr(unsafe.Pointer(&pvct)), uintptr(unsafe.Pointer(&cur)), uintptr(unsafe.Pointer(&max)))
		if rr != 0 {
			val = int32(cur)
		}
		for _, pm := range phys {
			procDestroyPhysicalMonitor.Call(uintptr(pm.Handle))
		}
		return val
	}
	return -1 // no physical monitor handle at all
}

// ddcLoop keeps the cache fresh on its own cadence: DDC transactions are slow
// (tens to hundreds of ms, seconds when the monitor is off) and must never
// stall the 1 Hz CSV loop.
func ddcLoop() {
	for {
		ddcPower.Store(queryDDCPower())
		time.Sleep(2 * time.Second)
	}
}

// -----------------------------------------------------------------------------

type sample struct {
	monitors    int
	qdcPaths    int
	targetAvail int
	ddcPower    int
	idleMS      int64
	fgVis       int
}

func takeSample() sample {
	qdc, avail := -1, -1
	if paths, err := activePaths(); err == nil {
		qdc = len(paths)
		if qdc > 0 {
			avail = int(paths[0].TargetInfo.TargetAvailable)
		}
	}
	return sample{
		monitors:    monitorCount(),
		qdcPaths:    qdc,
		targetAvail: avail,
		ddcPower:    int(ddcPower.Load()),
		idleMS:      idleMS(),
		fgVis:       foregroundPresent(),
	}
}

func (s sample) csv() string {
	return fmt.Sprintf("%s,%d,%d,%d,%d,%d,%d",
		time.Now().Format(time.RFC3339), s.monitors, s.qdcPaths, s.targetAvail,
		s.ddcPower, s.idleMS, s.fgVis)
}

const header = "timestamp,monitors_active,qdc_paths,target_available,ddc_power,idle_ms,foreground_present"

func main() {
	out := flag.String("out", "montest.csv", "CSV output path")
	interval := flag.Duration("interval", time.Second, "sample interval")
	once := flag.Bool("once", false, "print one reading and exit (CI smoke test)")
	flag.Parse()

	ddcPower.Store(-3) // not sampled yet
	fmt.Println(describePaths())

	if *once {
		ddcPower.Store(queryDDCPower())
		fmt.Println(header)
		fmt.Println(takeSample().csv())
		return
	}

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create:", err)
		os.Exit(1)
	}
	defer f.Close()

	go ddcLoop()

	fmt.Fprintln(f, header)
	fmt.Println(header)
	fmt.Printf("Logging every %s to %s.\n", *interval, *out)
	fmt.Println("Now physically power your monitor OFF for ~30s, then back ON. Ctrl+C to stop.")

	lastPaths := -2 // sentinel: neither a real count nor the -1 error value
	t := time.NewTicker(*interval)
	defer t.Stop()
	for range t.C {
		s := takeSample()
		line := s.csv()
		fmt.Fprintln(f, line)
		_ = f.Sync()
		fmt.Println(line)
		if s.qdcPaths != lastPaths {
			if lastPaths != -2 {
				fmt.Println(describePaths())
			}
			lastPaths = s.qdcPaths
		}
	}
}
