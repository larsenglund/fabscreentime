//go:build windows

package agent

import (
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The network's name ("Fabnet" for a Wi-Fi network, "Network 8" for a wired
// one) lives in Windows' Network List Manager. Its registry backing store is
// admin-only and the Wi-Fi API needs location permission on Windows 11, but the
// NLM COM interface answers a standard user — so it is called directly here,
// through raw vtable calls (no PowerShell, no WMI).
//
// The vtable indices below are fixed ABI from netlistmgr.h. Every interface
// derives from IDispatch, whose 7 methods (IUnknown's 3 + 4) come first, so
// each interface's own methods start at index 7.

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	oleaut32             = windows.NewLazySystemDLL("oleaut32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procSysFreeString    = oleaut32.NewProc("SysFreeString")

	clsidNetworkListManager = windows.GUID{Data1: 0xDCB00C01, Data2: 0x570F, Data3: 0x4A9B,
		Data4: [8]byte{0x8D, 0x69, 0x19, 0x9F, 0xDB, 0xA5, 0x72, 0x3B}}
	iidINetworkListManager = windows.GUID{Data1: 0xDCB00000, Data2: 0x570F, Data3: 0x4A9B,
		Data4: [8]byte{0x8D, 0x69, 0x19, 0x9F, 0xDB, 0xA5, 0x72, 0x3B}}
)

const (
	clsctxAll = 0x17

	vtRelease                  = 2
	vtNLMGetNetworkConnections = 9  // INetworkListManager::GetNetworkConnections
	vtEnumConnNext             = 8  // IEnumNetworkConnections::Next
	vtConnGetNetwork           = 7  // INetworkConnection::GetNetwork
	vtConnGetAdapterID         = 12 // INetworkConnection::GetAdapterId
	vtNetworkGetName           = 7  // INetwork::GetName
)

// comObject is a COM interface pointer: its first word points at the vtable.
type comObject struct{ vtbl *[32]uintptr }

func (o *comObject) call(method int, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[method], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return r
}

func (o *comObject) release() { o.call(vtRelease) }

// networkNameForAdapter returns the NLM name of the network the adapter with
// this interface GUID ("{...}", as IP_ADAPTER_ADDRESSES.AdapterName gives it)
// is connected to, or "".
func networkNameForAdapter(adapterGUID string) string {
	adapterGUID = strings.ToUpper(strings.TrimSpace(adapterGUID))
	if adapterGUID == "" {
		return ""
	}
	// COM state is per OS thread: pin this goroutine to one for the calls.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	switch err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err {
	case nil, syscall.Errno(1): // S_OK, S_FALSE (already initialized): balance with CoUninitialize
		defer windows.CoUninitialize()
	default:
		// RPC_E_CHANGED_MODE: the thread is already in another apartment, which
		// still works for these calls; anything else means COM is unavailable.
		if uint32(err.(syscall.Errno)) != 0x80010106 {
			return ""
		}
	}

	var nlm *comObject
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidNetworkListManager)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidINetworkListManager)), uintptr(unsafe.Pointer(&nlm)))
	if hr != 0 || nlm == nil {
		return ""
	}
	defer nlm.release()

	var enum *comObject
	if nlm.call(vtNLMGetNetworkConnections, uintptr(unsafe.Pointer(&enum))) != 0 || enum == nil {
		return ""
	}
	defer enum.release()

	for i := 0; i < 64; i++ {
		var conn *comObject
		var fetched uint32
		if enum.call(vtEnumConnNext, 1, uintptr(unsafe.Pointer(&conn)), uintptr(unsafe.Pointer(&fetched))) != 0 ||
			fetched == 0 || conn == nil {
			return "" // S_FALSE: no more connections
		}
		name, match := connectionNetworkName(conn, adapterGUID)
		conn.release()
		if match {
			return name
		}
	}
	return ""
}

// connectionNetworkName reports whether conn runs over the given adapter and,
// if so, its network's name.
func connectionNetworkName(conn *comObject, adapterGUID string) (string, bool) {
	var id windows.GUID
	if conn.call(vtConnGetAdapterID, uintptr(unsafe.Pointer(&id))) != 0 || id.String() != adapterGUID {
		return "", false
	}
	var network *comObject
	if conn.call(vtConnGetNetwork, uintptr(unsafe.Pointer(&network))) != 0 || network == nil {
		return "", true
	}
	defer network.release()
	var bstr *uint16
	if network.call(vtNetworkGetName, uintptr(unsafe.Pointer(&bstr))) != 0 || bstr == nil {
		return "", true
	}
	defer procSysFreeString.Call(uintptr(unsafe.Pointer(bstr)))
	return cleanString(windows.UTF16PtrToString(bstr)), true
}
