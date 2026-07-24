//go:build windows

package agent

import (
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DDC/CI power watcher — the empirically validated physical-power-off signal.
//
// On the household hardware (MONTEST-RESULTS.md) a physically powered-off
// monitor NEVER leaves the display topology, so the §4.4a connection count
// cannot be the only signal. What does fire, on every panel/connector tested,
// is the dxva2 probe: over DisplayPort the physical-monitor handle disappears
// (ddcNoHandle); over HDMI the handle stays powered and the VCP 0xD6 reply
// flips to standby/off (2..5). ddcSaysOff in core.go interprets the value.
//
// DDC transactions are slow (tens–hundreds of ms, seconds against a dead
// monitor), so the watcher polls on its own goroutine at a ~2 s cadence and
// the sampler only ever reads the cached value.

const (
	vcpPowerMode = 0xD6 // DDC/CI VCP code: display power mode

	ddcCadence = 2 * time.Second
)

var (
	dxva2 = windows.NewLazySystemDLL("dxva2.dll")

	procEnumDisplayMonitors             = user32.NewProc("EnumDisplayMonitors")
	procGetNumberOfPhysicalMonitors     = dxva2.NewProc("GetNumberOfPhysicalMonitorsFromHMONITOR")
	procGetPhysicalMonitorsFromHMONITOR = dxva2.NewProc("GetPhysicalMonitorsFromHMONITOR")
	procDestroyPhysicalMonitor          = dxva2.NewProc("DestroyPhysicalMonitor")
	procGetVCPFeatureAndVCPFeatureReply = dxva2.NewProc("GetVCPFeatureAndVCPFeatureReply")
)

// physicalMonitor is Win32 PHYSICAL_MONITOR (handle + description).
type physicalMonitor struct {
	Handle windows.Handle
	Desc   [128]uint16
}

func init() {
	if unsafe.Sizeof(physicalMonitor{}) != 264 {
		panic("PHYSICAL_MONITOR size mismatch")
	}
}

type ddcWatcher struct {
	power atomic.Int32 // latest raw reading (DDCPower encoding, see sampler.go)
	armed atomic.Bool  // a VCP power reply has been seen this session
}

// newDDCWatcher starts the background poll loop. If dxva2 is unavailable the
// watcher stays at "not sampled" forever, which the derivation rule treats as
// no testimony (fail toward counting screentime, never silently zeroing it).
func newDDCWatcher() *ddcWatcher {
	w := &ddcWatcher{}
	w.power.Store(DDCNotSampled)
	if err := procGetPhysicalMonitorsFromHMONITOR.Find(); err != nil {
		return w
	}
	w.observe(queryDDCPower()) // synchronous first fill so the first sample sees a real value
	go w.loop()
	return w
}

func (w *ddcWatcher) observe(v int32) {
	w.power.Store(v)
	if v >= 1 && v <= 5 {
		// Any successful VCP power reply proves DDC works on this hardware,
		// which is what licenses trusting a later handle-loss as "off".
		w.armed.Store(true)
	}
}

func (w *ddcWatcher) loop() {
	for {
		time.Sleep(ddcCadence)
		w.observe(queryDDCPower())
	}
}

func (w *ddcWatcher) read() (power int, armed bool) {
	return int(w.power.Load()), w.armed.Load()
}

// ddcMu serializes queryDDCPower: the enum buffer is shared package state and
// DDC I2C transactions must not interleave (multiple samplers exist in tests).
var ddcMu sync.Mutex

// ddcEnumResult/ddcEnumCallback: NewCallback allocations are permanent, so the
// callback is created exactly once; access is serialized by ddcMu.
var ddcEnumResult []uintptr
var ddcEnumCallback = windows.NewCallback(func(hmon, hdc, rect, lparam uintptr) uintptr {
	ddcEnumResult = append(ddcEnumResult, hmon)
	return 1 // continue enumeration
})

// queryDDCPower re-enumerates physical monitors (handles go stale across power
// events) and asks the first one for its VCP 0xD6 power mode. Encoding is the
// Reading.DDCPower contract in sampler.go.
func queryDDCPower() int32 {
	ddcMu.Lock()
	defer ddcMu.Unlock()

	ddcEnumResult = ddcEnumResult[:0]
	procEnumDisplayMonitors.Call(0, 0, ddcEnumCallback, 0)

	for _, hm := range ddcEnumResult {
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
		val := int32(DDCQueryFailed)
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
	return DDCNoHandle
}
