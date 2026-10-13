package agent

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"log"
	"net"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

// Computer inventory (CPU, RAM, GPUs, motherboard, drives, network) for the
// dashboard's "Computer" card. Collection is platform-specific
// (sysinfo_windows.go); everything here is the pure decoding and naming logic,
// kept separate so it is unit-tested on every platform.
//
// On Windows it deliberately uses only plain Win32 calls — the SMBIOS firmware
// table, the registry, SetupAPI and disk-query IOCTLs — never PowerShell or WMI:
// a hidden, unsigned background exe spawning PowerShell is exactly the pattern
// behavioral antivirus flags, and this agent has been quarantined for less.

// SystemInfoCollector returns the inventory collector the agent runs
// periodically. serverURL is only used to find which local address and adapter
// reach the dashboard server.
func SystemInfoCollector(serverURL string) func() *shared.SystemInfo {
	return func() *shared.SystemInfo { return CollectSystemInfo(serverURL, time.Now()) }
}

// CollectSystemInfo gathers the inventory. Each probe is isolated, so one that
// fails or panics on an unusual machine only blanks its own part.
func CollectSystemInfo(serverURL string, now time.Time) *shared.SystemInfo {
	info := &shared.SystemInfo{CollectedAt: now.Unix()}
	var ip net.IP
	probe("network route", func() { ip = primaryIPv4(serverURL) })
	collectPlatform(info, ip)
	return info
}

// probe runs one inventory probe, logging (not propagating) a panic.
func probe(what string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("system info: %s probe panicked (skipped): %v\n%s", what, r, debug.Stack())
		}
	}()
	fn()
}

// systemInfoKey fingerprints an inventory's content (ignoring when it was
// collected), so the agent only re-sends it when something actually changed.
func systemInfoKey(info *shared.SystemInfo) string {
	c := *info
	c.CollectedAt = 0
	b, _ := json.Marshal(c)
	return string(b)
}

// primaryIPv4 returns the local IPv4 address the OS would use to reach the
// server. A UDP "dial" sends no packets — it only asks for the route.
func primaryIPv4(serverURL string) net.IP {
	u, err := url.Parse(serverURL)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.Dial("udp4", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil
	}
	defer c.Close()
	addr, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP.IsLoopback() || addr.IP.IsUnspecified() {
		return nil
	}
	return addr.IP.To4()
}

// --- SMBIOS -------------------------------------------------------------------
//
// The SMBIOS (DMI) table is the firmware's own description of the hardware:
// the one place RAM speed/type, slot count and the motherboard model are all
// available without admin rights. Layout per DMTF DSP0134.

type smbiosStruct struct {
	typ     byte
	data    []byte   // the formatted area, including the 4-byte header
	strings []string // the trailing string set; SMBIOS indexes it from 1
}

func (s smbiosStruct) u8(off int) (byte, bool) {
	if off >= len(s.data) {
		return 0, false
	}
	return s.data[off], true
}

func (s smbiosStruct) u16(off int) (uint16, bool) {
	if off+2 > len(s.data) {
		return 0, false
	}
	return binary.LittleEndian.Uint16(s.data[off:]), true
}

func (s smbiosStruct) u32(off int) (uint32, bool) {
	if off+4 > len(s.data) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(s.data[off:]), true
}

func (s smbiosStruct) handle() uint16 {
	h, _ := s.u16(2)
	return h
}

// str resolves the string-index byte at off; "" when absent or out of range.
func (s smbiosStruct) str(off int) string {
	i, ok := s.u8(off)
	if !ok || i == 0 || int(i) > len(s.strings) {
		return ""
	}
	return s.strings[i-1]
}

// parseRawSMBIOS unwraps the RawSMBIOSData blob Windows' GetSystemFirmwareTable
// returns (an 8-byte header whose bytes 4..8 are the table length) and parses
// the table inside.
func parseRawSMBIOS(raw []byte) []smbiosStruct {
	if len(raw) < 8 {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(raw[4:8]))
	if n > len(raw)-8 {
		n = len(raw) - 8
	}
	return parseSMBIOS(raw[8 : 8+n])
}

// parseSMBIOS splits an SMBIOS structure table. It stops at the end-of-table
// marker (type 127) or at the first malformed structure, keeping what it has.
func parseSMBIOS(table []byte) []smbiosStruct {
	var out []smbiosStruct
	i := 0
	for i+4 <= len(table) {
		typ, length := table[i], int(table[i+1])
		if length < 4 || i+length > len(table) {
			break
		}
		s := smbiosStruct{typ: typ, data: table[i : i+length]}
		// The string set: NUL-terminated strings, ended by an extra NUL. A
		// structure with no strings is followed by two NULs.
		j := i + length
		for j < len(table) {
			end := bytes.IndexByte(table[j:], 0)
			if end < 0 {
				j = len(table)
				break
			}
			if end == 0 {
				j++
				break
			}
			s.strings = append(s.strings, cleanString(string(table[j:j+end])))
			j += end + 1
		}
		if len(s.strings) == 0 {
			j++ // the second NUL of an empty string set
		}
		if typ == 127 {
			break
		}
		out = append(out, s)
		i = j
	}
	return out
}

// cleanString trims padding and drops non-printable bytes, which firmware and
// drive descriptors are full of.
func cleanString(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// smbiosPlaceholders are the stock strings board vendors leave in fields they
// never filled in; showing them would just be noise.
var smbiosPlaceholders = []string{
	"to be filled by o.e.m.", "system product name", "system manufacturer",
	"system version", "default string", "not applicable", "not specified",
	"not available", "o.e.m.", "oem", "none", "n/a", "na", "unknown",
	"123456789", "0123456789", "type1productconfigid", "type2 - board product name1",
	"x.x", "undefined", "base board product name", "base board manufacturer",
	"invalid", "empty",
}

// meaningful returns s, or "" if it is a firmware placeholder.
func meaningful(s string) string {
	s = cleanString(s)
	l := strings.ToLower(s)
	for _, p := range smbiosPlaceholders {
		if l == p {
			return ""
		}
	}
	if strings.Trim(l, "0 ") == "" {
		return ""
	}
	return s
}

// makerNames shortens the legal company names hardware reports to the names
// people know. Matched as a case-insensitive prefix.
var makerNames = []struct{ prefix, name string }{
	{"asustek", "ASUS"}, {"asus", "ASUS"},
	{"micro-star", "MSI"}, {"msi", "MSI"},
	{"gigabyte", "Gigabyte"},
	{"asrock", "ASRock"},
	{"lenovo", "Lenovo"},
	{"dell", "Dell"},
	{"hewlett-packard", "HP"}, {"hp ", "HP"}, // "hp " also matches a bare "HP" (see friendlyMaker)
	{"acer", "Acer"},
	{"samsung", "Samsung"},
	{"microsoft", "Microsoft"},
	{"apple", "Apple"},
	{"intel", "Intel"},
	{"advanced micro devices", "AMD"}, {"amd", "AMD"},
	{"nvidia", "NVIDIA"},
	{"biostar", "Biostar"},
	{"evga", "EVGA"},
	{"razer", "Razer"},
	{"toshiba", "Toshiba"},
	{"fujitsu", "Fujitsu"},
	{"medion", "Medion"},
	{"huawei", "Huawei"},
	{"framework", "Framework"},
	{"kingston", "Kingston"},
	{"corsair", "Corsair"},
	{"g.skill", "G.Skill"}, {"g skill", "G.Skill"},
	{"crucial", "Crucial"},
	{"micron", "Micron"},
	{"sk hynix", "SK hynix"}, {"hynix", "SK hynix"},
	{"team", "Team Group"},
	{"patriot", "Patriot"},
	{"adata", "ADATA"}, {"a-data", "ADATA"},
}

// friendlyMaker maps a reported manufacturer to its familiar short name,
// passing unknown names through cleaned.
func friendlyMaker(s string) string {
	s = meaningful(s)
	l := strings.ToLower(s)
	for _, m := range makerNames {
		if l == strings.TrimSpace(m.prefix) || strings.HasPrefix(l, m.prefix) {
			return m.name
		}
	}
	return s
}

// jedecMakers maps the JEDEC manufacturer codes many boards report for RAM
// instead of a name (both byte orders occur in the wild).
var jedecMakers = map[string]string{
	"80CE": "Samsung", "CE00": "Samsung",
	"80AD": "SK hynix", "AD00": "SK hynix",
	"802C": "Micron", "2C00": "Micron",
	"859B": "Crucial", "9B85": "Crucial",
	"0198": "Kingston", "9801": "Kingston",
	"04CD": "G.Skill", "CD04": "G.Skill",
	"029E": "Corsair", "9E02": "Corsair",
	"04CB": "ADATA", "CB04": "ADATA",
}

// memoryMaker names a RAM module's maker from its manufacturer field (decoding
// JEDEC codes), falling back to its part number: plenty of boards report the
// manufacturer as "Unknown" even for a stick whose part number names it.
func memoryMaker(field, part string) string {
	if s := meaningful(field); s != "" {
		code := strings.ToUpper(strings.TrimPrefix(strings.ReplaceAll(s, " ", ""), "0x"))
		if len(code) >= 4 {
			if name, ok := jedecMakers[code[:4]]; ok {
				return name
			}
		}
		if !isHex(code) { // an unknown code means nothing to a reader
			return friendlyMaker(s)
		}
	}
	p := strings.ToUpper(meaningful(part))
	for _, m := range memoryPartPrefixes {
		if strings.HasPrefix(p, m.prefix) {
			return m.name
		}
	}
	return ""
}

// memoryPartPrefixes are the part-number prefixes of the common RAM brands.
var memoryPartPrefixes = []struct{ prefix, name string }{
	{"F3-", "G.Skill"}, {"F4-", "G.Skill"}, {"F5-", "G.Skill"},
	{"CMK", "Corsair"}, {"CMW", "Corsair"}, {"CMT", "Corsair"}, {"CMH", "Corsair"},
	{"CMR", "Corsair"}, {"CMD", "Corsair"}, {"CMG", "Corsair"}, {"CMN", "Corsair"},
	{"CMSX", "Corsair"}, {"CMSO", "Corsair"}, {"CMV", "Corsair"}, {"CMZ", "Corsair"},
	{"CMP", "Corsair"}, {"CMX", "Corsair"}, {"CMY", "Corsair"}, {"CMU", "Corsair"},
	{"KHX", "Kingston"}, {"KF", "Kingston"}, {"KVR", "Kingston"}, {"HX4", "Kingston"},
	{"HX5", "Kingston"}, {"HX3", "Kingston"}, {"KCP", "Kingston"}, {"KSM", "Kingston"},
	{"BL", "Crucial"}, {"CT", "Crucial"},
	{"M378", "Samsung"}, {"M471", "Samsung"}, {"M391", "Samsung"}, {"M393", "Samsung"},
	{"M323", "Samsung"}, {"M425", "Samsung"}, {"M474", "Samsung"},
	{"HMA", "SK hynix"}, {"HMT", "SK hynix"}, {"HMC", "SK hynix"},
	{"MTA", "Micron"}, {"MTC", "Micron"},
	{"TED", "Team Group"}, {"TLZ", "Team Group"}, {"TDP", "Team Group"}, {"TF", "Team Group"},
	{"AX4", "ADATA"}, {"AD4", "ADATA"}, {"AX5", "ADATA"}, {"AD5", "ADATA"},
	{"PSD", "Patriot"}, {"PV", "Patriot"},
}

// memorySlotLabel names a module's slot. Boards often repeat the device locator
// per channel ("DIMM 1" on both channels), so the bank is folded in.
func memorySlotLabel(bank, device string) string {
	bank, device = meaningful(bank), meaningful(device)
	switch {
	case bank == "":
		return device
	case device == "":
		return bank
	case strings.Contains(strings.ToLower(device), strings.ToLower(bank)):
		return device // already says which bank
	}
	return bank + " / " + device
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789ABCDEF", r) {
			return false
		}
	}
	return true
}

// memoryTypeName decodes the SMBIOS type-17 Memory Type byte.
func memoryTypeName(code byte) string {
	switch code {
	case 0x0F:
		return "SDRAM"
	case 0x12:
		return "DDR"
	case 0x13, 0x14:
		return "DDR2"
	case 0x18:
		return "DDR3"
	case 0x1A:
		return "DDR4"
	case 0x1B:
		return "LPDDR"
	case 0x1C:
		return "LPDDR2"
	case 0x1D:
		return "LPDDR3"
	case 0x1E:
		return "LPDDR4"
	case 0x22:
		return "DDR5"
	case 0x23:
		return "LPDDR5"
	}
	return ""
}

// memoryModuleSize decodes a type-17 Size word (with its Extended Size
// fallback). 0 means an empty slot or an unknown size.
func memoryModuleSize(s smbiosStruct) uint64 {
	size, ok := s.u16(0x0C)
	if !ok || size == 0 || size == 0xFFFF {
		return 0
	}
	if size == 0x7FFF {
		ext, ok := s.u32(0x1C)
		if !ok {
			return 0
		}
		return uint64(ext&0x7FFFFFFF) << 20 // MB
	}
	if size&0x8000 != 0 {
		return uint64(size&0x7FFF) << 10 // KB
	}
	return uint64(size) << 20 // MB
}

// memorySpeed reads a type-17 speed word (MT/s), following the 0xFFFF escape to
// its 32-bit extended field.
func memorySpeed(s smbiosStruct, off, extOff int) int {
	v, ok := s.u16(off)
	if !ok || v == 0 {
		return 0
	}
	if v == 0xFFFF {
		ext, ok := s.u32(extOff)
		if !ok {
			return 0
		}
		return int(ext & 0x7FFFFFFF)
	}
	return int(v)
}

// buildMemoryInfo summarizes the type-16/17 structures: installed modules, slot
// count, and the RAM type and running speed.
func buildMemoryInfo(structs []smbiosStruct) *shared.MemoryInfo {
	// Only count devices on arrays used for system memory (type 16, Use = 3), so
	// flash/video-memory arrays some firmware lists don't inflate the slot count.
	sysArrays := map[uint16]bool{}
	sawArray := false
	for _, s := range structs {
		if s.typ == 16 {
			sawArray = true
			if use, ok := s.u8(0x05); ok && use == 0x03 {
				sysArrays[s.handle()] = true
			}
		}
	}
	m := &shared.MemoryInfo{}
	for _, s := range structs {
		if s.typ != 17 {
			continue
		}
		if sawArray {
			if arr, ok := s.u16(0x04); ok && !sysArrays[arr] {
				continue
			}
		}
		m.Slots++
		size := memoryModuleSize(s)
		if size == 0 {
			continue // empty slot
		}
		typeCode, _ := s.u8(0x12)
		mod := shared.MemoryModule{
			Slot:         memorySlotLabel(s.str(0x11), s.str(0x10)),
			SizeBytes:    size,
			Type:         memoryTypeName(typeCode),
			RatedMTs:     memorySpeed(s, 0x15, 0x54),
			SpeedMTs:     memorySpeed(s, 0x20, 0x58),
			Manufacturer: memoryMaker(s.str(0x17), s.str(0x1A)),
			PartNumber:   meaningful(s.str(0x1A)),
		}
		if mod.SpeedMTs == 0 {
			mod.SpeedMTs = mod.RatedMTs
		}
		m.Modules = append(m.Modules, mod)
		m.TotalBytes += size
		if m.Type == "" {
			m.Type = mod.Type
		}
		if mod.SpeedMTs > 0 && (m.SpeedMTs == 0 || mod.SpeedMTs < m.SpeedMTs) {
			m.SpeedMTs = mod.SpeedMTs // mixed sticks all run at the slowest one's speed
		}
	}
	if m.Slots < len(m.Modules) {
		m.Slots = len(m.Modules)
	}
	if m.TotalBytes == 0 && m.Slots == 0 {
		return nil
	}
	return m
}

// buildBoardInfo reads the BIOS (type 0), system (type 1) and baseboard (type 2)
// structures.
func buildBoardInfo(structs []smbiosStruct) *shared.BoardInfo {
	b := &shared.BoardInfo{}
	for _, s := range structs {
		switch s.typ {
		case 0:
			if b.BIOS == "" {
				b.BIOS = meaningful(s.str(0x05))
			}
		case 1:
			if b.SystemMaker == "" {
				b.SystemMaker = friendlyMaker(s.str(0x04))
				b.SystemModel = meaningful(s.str(0x05))
				// Lenovo puts a machine-type code ("20XW0055MX") in Product Name
				// and the name people know ("ThinkPad X1 Carbon Gen 9") in Version.
				if b.SystemMaker == "Lenovo" {
					if v := meaningful(s.str(0x06)); v != "" {
						b.SystemModel = v
					}
				}
			}
		case 2:
			if b.Manufacturer == "" {
				b.Manufacturer = friendlyMaker(s.str(0x04))
				b.Product = meaningful(s.str(0x05))
			}
		}
	}
	if *b == (shared.BoardInfo{}) {
		return nil
	}
	return b
}

// cpuCounts sums physical cores and threads over populated processor sockets
// (type 4). Either is 0 when the firmware doesn't say.
func cpuCounts(structs []smbiosStruct) (cores, threads int) {
	for _, s := range structs {
		if s.typ != 4 {
			continue
		}
		if status, ok := s.u8(0x18); ok && status&0x40 == 0 {
			continue // empty socket
		}
		c, _ := s.u8(0x23)
		t, _ := s.u8(0x25)
		cc, tt := int(c), int(t)
		if c == 0xFF {
			if c2, ok := s.u16(0x2A); ok {
				cc = int(c2)
			}
		}
		if t == 0xFF {
			if t2, ok := s.u16(0x2E); ok {
				tt = int(t2)
			}
		}
		cores += cc
		threads += tt
	}
	return cores, threads
}

// cpuVendor maps a CPUID vendor string to its maker.
func cpuVendor(id string) string {
	switch strings.TrimSpace(id) {
	case "GenuineIntel":
		return "Intel"
	case "AuthenticAMD":
		return "AMD"
	case "CentaurHauls":
		return "VIA"
	case "HygonGenuine":
		return "Hygon"
	}
	return friendlyMaker(id)
}

// osName renders the Windows version. Windows 11 still reports "Windows 10" in
// ProductName; build 22000 and later is Windows 11.
func osName(product, displayVersion, build string, ubr uint64) string {
	product = cleanString(product)
	if n, err := strconv.Atoi(build); err == nil && n >= 22000 {
		product = strings.Replace(product, "Windows 10", "Windows 11", 1)
	}
	out := product
	if displayVersion != "" {
		out += " " + displayVersion
	}
	if build != "" {
		b := build
		if ubr > 0 {
			b += "." + strconv.FormatUint(ubr, 10)
		}
		out += " (build " + b + ")"
	}
	return strings.TrimSpace(out)
}

// --- Drives ---------------------------------------------------------------------

// Windows STORAGE_BUS_TYPE values.
const (
	busSCSI              = 0x01
	busATA               = 0x03
	busUSB               = 0x07
	busRAID              = 0x08
	busSAS               = 0x0A
	busSATA              = 0x0B
	busSD                = 0x0C
	busMMC               = 0x0D
	busVirtual           = 0x0E
	busFileBackedVirtual = 0x0F
	busSpaces            = 0x10
	busNVMe              = 0x11
)

// busName names a storage bus, and whether a drive on it counts as an internal
// disk. USB/SD/virtual/Storage-Spaces disks are left out: they come and go, or
// are a view of drives already listed.
func busName(bus uint32) (string, bool) {
	switch bus {
	case busNVMe:
		return "NVMe", true
	case busSATA, busATA:
		return "SATA", true
	case busSAS:
		return "SAS", true
	case busSCSI:
		return "SCSI", true
	case busRAID:
		return "RAID", true
	case busUSB, busSD, busMMC, busVirtual, busFileBackedVirtual, busSpaces:
		return "", false
	}
	return "", true
}

// parseStorageDescriptor decodes a STORAGE_DEVICE_DESCRIPTOR (the reply to
// IOCTL_STORAGE_QUERY_PROPERTY / StorageDeviceProperty).
func parseStorageDescriptor(b []byte) (vendor, product string, bus uint32, removable, ok bool) {
	if len(b) < 32 {
		return "", "", 0, false, false
	}
	str := func(off uint32) string {
		if off == 0 || int(off) >= len(b) {
			return ""
		}
		end := bytes.IndexByte(b[off:], 0)
		if end < 0 {
			end = len(b) - int(off)
		}
		return cleanString(string(b[off : int(off)+end]))
	}
	le := binary.LittleEndian
	removable = b[10] != 0
	vendor = str(le.Uint32(b[12:]))
	product = str(le.Uint32(b[16:]))
	bus = le.Uint32(b[28:])
	return vendor, product, bus, removable, true
}

// driveBrands infers a drive's maker from its model string, which for SATA and
// NVMe drives is usually all the hardware reports. Checked in order; a token
// matches as a case-insensitive prefix of the model or of any word in it.
var driveBrands = []struct{ token, name string }{
	{"samsung", "Samsung"}, {"mzvl", "Samsung"}, {"mzvp", "Samsung"}, {"mz-", "Samsung"},
	{"wdc", "Western Digital"}, {"wd_black", "Western Digital"}, {"wds", "Western Digital"},
	{"western digital", "Western Digital"}, {"wd ", "Western Digital"},
	{"seagate", "Seagate"},
	{"crucial", "Crucial"},
	{"kingston", "Kingston"},
	{"sandisk", "SanDisk"},
	{"toshiba", "Toshiba"}, {"kioxia", "Kioxia"},
	{"intel", "Intel"}, {"ssdpe", "Intel"}, {"ssdsc", "Intel"},
	{"micron", "Micron"}, {"mtfd", "Micron"},
	{"sk hynix", "SK hynix"}, {"skhynix", "SK hynix"}, {"hynix", "SK hynix"}, {"hfs", "SK hynix"}, {"hfm", "SK hynix"},
	{"adata", "ADATA"}, {"xpg", "ADATA"},
	{"corsair", "Corsair"},
	{"sabrent", "Sabrent"},
	{"teamgroup", "Team Group"}, {"team", "Team Group"}, {"t-force", "Team Group"},
	{"hgst", "HGST"}, {"hitachi", "Hitachi"},
	{"pny", "PNY"},
	{"lexar", "Lexar"},
	{"patriot", "Patriot"},
	{"transcend", "Transcend"},
	{"spcc", "Silicon Power"}, {"silicon power", "Silicon Power"},
	{"netac", "Netac"},
	{"gigabyte", "Gigabyte"},
	{"msi", "MSI"},
	{"hp ssd", "HP"},
	{"lenovo", "Lenovo"},
	{"apple", "Apple"},
	{"maxtor", "Maxtor"},
	{"fujitsu", "Fujitsu"},
	{"phison", "Phison"},
	{"kingspec", "KingSpec"},
	{"verbatim", "Verbatim"},
}

// genericDriveVendors are vendor fields that name the protocol, not the maker.
var genericDriveVendors = map[string]bool{"": true, "ata": true, "nvme": true, "msft": true, "scsi": true}

// driveModel composes a drive's display model. Most drives put the whole model
// in the product field; SCSI-style ones split the maker into the vendor field.
func driveModel(vendor, product string) string {
	vendor, product = cleanString(vendor), cleanString(product)
	if genericDriveVendors[strings.ToLower(vendor)] ||
		strings.Contains(strings.ToLower(product), strings.ToLower(vendor)) {
		return product
	}
	return vendor + " " + product
}

// driveMaker names a drive's maker from its descriptor vendor and model.
func driveMaker(vendor, model string) string {
	if v := cleanString(vendor); !genericDriveVendors[strings.ToLower(v)] {
		if name := driveBrand(v); name != "" {
			return name
		}
		return friendlyMaker(v)
	}
	return driveBrand(model)
}

// driveBrand matches a vendor or model string against driveBrands.
func driveBrand(s string) string {
	m := strings.ToLower(cleanString(s))
	words := strings.Fields(m)
	for _, b := range driveBrands {
		if strings.HasPrefix(m, b.token) {
			return b.name
		}
		if strings.Contains(b.token, " ") {
			continue // multi-word tokens only match the start of the model
		}
		// OEM drives often name the maker mid-string ("PC SN730 NVMe WDC 512GB").
		for _, w := range words {
			if strings.HasPrefix(w, b.token) {
				return b.name
			}
		}
	}
	// Seagate model numbers: "ST" + digits (ST1000DM010); Crucial: "CT" + digits.
	if len(m) > 3 && m[2] >= '0' && m[2] <= '9' {
		switch m[:2] {
		case "st":
			return "Seagate"
		case "ct":
			return "Crucial"
		}
	}
	return ""
}

// --- GPUs -----------------------------------------------------------------------

// parsePCIInstance extracts the chip vendor and the board's subsystem vendor
// from a PCI device-instance ID such as
// `PCI\VEN_10DE&DEV_1C03&SUBSYS_85AE1043&REV_A1\4&2B8260C3&0&0008`.
func parsePCIInstance(id string) (ven, subsysVen uint16, ok bool) {
	u := strings.ToUpper(id)
	if !strings.HasPrefix(u, `PCI\`) {
		return 0, 0, false
	}
	hex16 := func(s string) (uint16, bool) {
		v, err := strconv.ParseUint(s, 16, 16)
		return uint16(v), err == nil
	}
	for _, part := range strings.Split(strings.SplitN(u[4:], `\`, 2)[0], "&") {
		switch {
		case strings.HasPrefix(part, "VEN_") && len(part) >= 8:
			ven, ok = hex16(part[4:8])
		case strings.HasPrefix(part, "SUBSYS_") && len(part) >= 15:
			// SUBSYS_ddddvvvv: subsystem device id, then subsystem vendor id.
			subsysVen, _ = hex16(part[11:15])
		}
	}
	return ven, subsysVen, ok
}

// pciVendors names PCI vendor IDs: the GPU chip makers, and the card makers
// (board partners and laptop OEMs) that appear as subsystem vendors.
var pciVendors = map[uint16]string{
	0x10DE: "NVIDIA", 0x1002: "AMD", 0x1022: "AMD", 0x8086: "Intel",
	0x5143: "Qualcomm", 0x1414: "Microsoft", 0x1A03: "ASPEED",
	0x15AD: "VMware", 0x1AF4: "Red Hat", 0x1234: "QEMU", 0x80EE: "VirtualBox",
	0x1043: "ASUS", 0x1462: "MSI", 0x1458: "Gigabyte", 0x3842: "EVGA",
	0x19DA: "Zotac", 0x1569: "Palit", 0x10B0: "Gainward", 0x1B4C: "Galax",
	0x7377: "Colorful", 0x196E: "PNY", 0x1DA2: "Sapphire", 0x174B: "Sapphire",
	0x1682: "XFX", 0x148C: "PowerColor", 0x1849: "ASRock", 0x107D: "Leadtek",
	0x1028: "Dell", 0x103C: "HP", 0x17AA: "Lenovo", 0x1025: "Acer",
	0x144D: "Samsung", 0x1179: "Toshiba", 0x1558: "Clevo", 0x152D: "Quanta",
	0x1734: "Fujitsu", 0x1D05: "Tongfang", 0x1D72: "Xiaomi", 0x1E83: "Huawei",
}

// gpuMaker returns the card maker from the subsystem vendor, or "" when it is
// unknown or just the chip maker (a reference/Founders Edition card).
func gpuMaker(ven, subsysVen uint16) string {
	if subsysVen == 0 || subsysVen == ven {
		return ""
	}
	return pciVendors[subsysVen]
}

// gpuVendor names the chip maker, preferring the PCI vendor ID over the
// driver's manufacturer string.
func gpuVendor(ven uint16, mfg string) string {
	if n, ok := pciVendors[ven]; ok {
		return n
	}
	return friendlyMaker(mfg)
}

// --- Network --------------------------------------------------------------------

// ifKindName names a network interface type (IANA ifType).
func ifKindName(ifType uint32) string {
	switch ifType {
	case 71:
		return "Wi-Fi"
	case 6:
		return "Ethernet"
	case 243, 244:
		return "Mobile broadband"
	case 23, 131, 53:
		return "VPN"
	}
	return ""
}
