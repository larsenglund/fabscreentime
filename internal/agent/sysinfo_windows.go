//go:build windows

package agent

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

// Windows inventory probes. Everything here works for a standard (non-admin)
// user and uses plain Win32 calls only — see the note in sysinfo.go.

var (
	procGetSystemFirmwareTable             = kernel32.NewProc("GetSystemFirmwareTable")
	procGetPhysicallyInstalledSystemMemory = kernel32.NewProc("GetPhysicallyInstalledSystemMemory")
)

// GUID_DEVCLASS_DISPLAY: the device-setup class for display adapters.
var guidDisplayClass = windows.GUID{
	Data1: 0x4d36e968, Data2: 0xe325, Data3: 0x11ce,
	Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18},
}

func collectPlatform(info *shared.SystemInfo, ip net.IP) {
	var smbios []smbiosStruct
	probe("smbios", func() { smbios = parseRawSMBIOS(readSMBIOS()) })
	probe("os", func() { info.OS = readOSName() })
	probe("cpu", func() { info.CPU = readCPU(smbios) })
	probe("memory", func() { info.Memory = readMemory(smbios) })
	probe("board", func() { info.Board = buildBoardInfo(smbios) })
	probe("board id", func() {
		if maker, code := boardIdentity(readPCIIDs()); code != "" {
			if info.Board == nil {
				info.Board = &shared.BoardInfo{}
			}
			info.Board.HWMaker, info.Board.HWCode = maker, code
		}
	})
	probe("gpu", func() { info.GPUs = readGPUs() })
	probe("drives", func() { info.Drives = readDrives() })
	probe("network", func() { info.Network = readNetwork(ip) })
}

// readSMBIOS returns the raw SMBIOS table ('RSMB' firmware table provider).
func readSMBIOS() []byte {
	const rsmb = 'R'<<24 | 'S'<<16 | 'M'<<8 | 'B'
	n, _, _ := procGetSystemFirmwareTable.Call(rsmb, 0, 0, 0)
	if n == 0 || n > 1<<20 {
		return nil
	}
	buf := make([]byte, n)
	got, _, _ := procGetSystemFirmwareTable.Call(rsmb, 0, uintptr(unsafe.Pointer(&buf[0])), n)
	if got == 0 || got > n {
		return nil
	}
	return buf[:got]
}

func readOSName() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	product, _, _ := k.GetStringValue("ProductName")
	display, _, _ := k.GetStringValue("DisplayVersion")
	if display == "" {
		display, _, _ = k.GetStringValue("ReleaseId")
	}
	build, _, _ := k.GetStringValue("CurrentBuild")
	ubr, _, _ := k.GetIntegerValue("UBR")
	return osName(product, display, build, ubr)
}

func readCPU(smbios []smbiosStruct) *shared.CPUInfo {
	c := &shared.CPUInfo{Threads: runtime.NumCPU()}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE); err == nil {
		name, _, _ := k.GetStringValue("ProcessorNameString")
		vendor, _, _ := k.GetStringValue("VendorIdentifier")
		k.Close()
		c.Model = cleanString(name)
		c.Vendor = cpuVendor(vendor)
	}
	if cores, threads := cpuCounts(smbios); cores > 0 {
		c.Cores = cores
		if threads > 0 {
			c.Threads = threads
		}
	}
	if c.Model == "" && c.Threads == 0 {
		return nil
	}
	return c
}

func readMemory(smbios []smbiosStruct) *shared.MemoryInfo {
	m := buildMemoryInfo(smbios)
	if m == nil {
		m = &shared.MemoryInfo{}
	}
	if m.TotalBytes == 0 {
		var kb uint64
		if r, _, _ := procGetPhysicallyInstalledSystemMemory.Call(uintptr(unsafe.Pointer(&kb))); r != 0 {
			m.TotalBytes = kb << 10
		}
	}
	if m.TotalBytes == 0 {
		return nil
	}
	return m
}

// readGPUs lists the PCI display adapters present. Software and virtual
// displays (Microsoft Basic Display, remote-desktop and streaming adapters)
// aren't on the PCI bus, so they drop out naturally.
func readGPUs() []shared.GPUInfo {
	devs, err := windows.SetupDiGetClassDevsEx(&guidDisplayClass, "", 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return nil
	}
	defer devs.Close()
	var out []shared.GPUInfo
	for i := 0; i < 16; i++ {
		di, err := devs.EnumDeviceInfo(i)
		if err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
				break
			}
			continue
		}
		id, err := devs.DeviceInstanceID(di)
		if err != nil {
			continue
		}
		p, ok := parsePCIInstance(id)
		if !ok {
			continue
		}
		model := regString(devs.DeviceRegistryProperty(di, windows.SPDRP_DEVICEDESC))
		mfg := regString(devs.DeviceRegistryProperty(di, windows.SPDRP_MFG))
		g := shared.GPUInfo{
			Model:     cleanString(model),
			Vendor:    gpuVendor(p.ven, mfg),
			Maker:     gpuMaker(p.ven, p.subVen),
			VRAMBytes: gpuVRAM(devs, di),
		}
		if g.Model == "" {
			continue
		}
		out = append(out, g)
	}
	return out
}

// readPCIIDs lists the IDs of every PCI function present (for boardIdentity).
func readPCIIDs() []pciID {
	devs, err := windows.SetupDiGetClassDevsEx(nil, "PCI", 0, windows.DIGCF_ALLCLASSES|windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return nil
	}
	defer devs.Close()
	var out []pciID
	for i := 0; i < 1024; i++ {
		di, err := devs.EnumDeviceInfo(i)
		if err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
				break
			}
			continue
		}
		if id, err := devs.DeviceInstanceID(di); err == nil {
			if p, ok := parsePCIInstance(id); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

func regString(v any, err error) string {
	if err != nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// gpuVRAM reads the adapter's dedicated memory from its driver key. Newer
// drivers publish a 64-bit value; the older 32-bit one caps at 4 GB, so it is
// only the fallback (and may be REG_DWORD or REG_BINARY depending on vendor).
func gpuVRAM(devs windows.DevInfo, di *windows.DevInfoData) uint64 {
	h, err := devs.OpenDevRegKey(di, windows.DICS_FLAG_GLOBAL, 0, windows.DIREG_DRV, windows.KEY_READ)
	if err != nil {
		return 0
	}
	k := registry.Key(h)
	defer k.Close()
	if v, _, err := k.GetIntegerValue("HardwareInformation.qwMemorySize"); err == nil && v > 0 {
		return v
	}
	if v, _, err := k.GetIntegerValue("HardwareInformation.MemorySize"); err == nil && v > 0 {
		return v
	}
	if b, _, err := k.GetBinaryValue("HardwareInformation.MemorySize"); err == nil && len(b) >= 4 {
		return uint64(binary.LittleEndian.Uint32(b))
	}
	return 0
}

const (
	ioctlStorageQueryProperty   = 0x002D1400 // IOCTL_STORAGE_QUERY_PROPERTY
	ioctlDiskGetDriveGeometryEx = 0x000700A0 // IOCTL_DISK_GET_DRIVE_GEOMETRY_EX
	storageDeviceProperty       = 0
	storageSeekPenaltyProperty  = 7
)

// readDrives describes the internal physical disks. Each is opened with NO
// read/write access — only the descriptor queries above, which Windows allows a
// standard user and which never touch the disk's contents.
func readDrives() []shared.DriveInfo {
	var out []shared.DriveInfo
	for i := 0; i < 32; i++ {
		path, _ := windows.UTF16PtrFromString(fmt.Sprintf(`\\.\PhysicalDrive%d`, i))
		h, err := windows.CreateFile(path, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			continue
		}
		d, ok := describeDrive(h)
		windows.CloseHandle(h)
		if ok {
			out = append(out, d)
		}
	}
	return out
}

func describeDrive(h windows.Handle) (shared.DriveInfo, bool) {
	desc, ok := storageQuery(h, storageDeviceProperty, 1024)
	if !ok {
		return shared.DriveInfo{}, false
	}
	vendor, product, bus, removable, ok := parseStorageDescriptor(desc)
	busLabel, internal := busName(bus)
	if !ok || removable || !internal {
		return shared.DriveInfo{}, false
	}
	d := shared.DriveInfo{
		Model: driveModel(vendor, product),
		Maker: driveMaker(vendor, product),
		Bus:   busLabel,
	}
	if d.Model == "" {
		return shared.DriveInfo{}, false
	}
	if bus == busNVMe {
		d.Media = "SSD"
	} else if sp, ok := storageQuery(h, storageSeekPenaltyProperty, 16); ok && len(sp) >= 9 {
		// DEVICE_SEEK_PENALTY_DESCRIPTOR.IncursSeekPenalty: spinning disks seek.
		if sp[8] != 0 {
			d.Media = "HDD"
		} else {
			d.Media = "SSD"
		}
	}
	geo := make([]byte, 256)
	var n uint32
	if windows.DeviceIoControl(h, ioctlDiskGetDriveGeometryEx, nil, 0, &geo[0], uint32(len(geo)), &n, nil) == nil && n >= 32 {
		d.SizeBytes = binary.LittleEndian.Uint64(geo[24:32]) // DISK_GEOMETRY_EX.DiskSize
	}
	return d, true
}

// storageQuery runs IOCTL_STORAGE_QUERY_PROPERTY (standard query) for one
// property and returns the reply.
func storageQuery(h windows.Handle, property uint32, size int) ([]byte, bool) {
	var q [12]byte // STORAGE_PROPERTY_QUERY{PropertyId, QueryType=PropertyStandardQuery, AdditionalParameters}
	binary.LittleEndian.PutUint32(q[0:], property)
	out := make([]byte, size)
	var n uint32
	if err := windows.DeviceIoControl(h, ioctlStorageQueryProperty, &q[0], uint32(len(q)), &out[0], uint32(len(out)), &n, nil); err != nil {
		return nil, false
	}
	return out[:n], true
}

// readNetwork describes the adapter that owns ip (the address used to reach the
// dashboard server), and the name of the network it is on (nlm_windows.go).
func readNetwork(ip net.IP) *shared.NetworkInfo {
	if ip == nil {
		return nil
	}
	size := uint32(16 << 10)
	var buf []byte
	for attempt := 0; attempt < 3; attempt++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC,
			windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER,
			0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) || attempt == 2 {
			return &shared.NetworkInfo{IPv4: ip.String()}
		}
	}
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		for u := a.FirstUnicastAddress; u != nil; u = u.Next {
			if !u.Address.IP().Equal(ip) {
				continue
			}
			n := &shared.NetworkInfo{
				Kind:    ifKindName(a.IfType),
				Adapter: cleanString(windows.UTF16PtrToString(a.Description)),
				IPv4:    ip.String(),
			}
			probe("network name", func() { n.Name = networkNameForAdapter(windows.BytePtrToString(a.AdapterName)) })
			if s := a.TransmitLinkSpeed; s > 0 && s != ^uint64(0) {
				n.LinkMbps = int(s / 1_000_000)
			}
			return n
		}
	}
	return &shared.NetworkInfo{IPv4: ip.String()}
}
