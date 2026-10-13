package agent

import (
	"encoding/binary"
	"testing"
)

// smbiosEntry builds one SMBIOS structure from its formatted area f (offsets as
// in the spec, header bytes 0..3 included but overwritten) and its strings.
func smbiosEntry(typ byte, handle uint16, f []byte, strs ...string) []byte {
	f[0], f[1] = typ, byte(len(f))
	binary.LittleEndian.PutUint16(f[2:], handle)
	b := append([]byte{}, f...)
	if len(strs) == 0 {
		return append(b, 0, 0)
	}
	for _, s := range strs {
		b = append(append(b, s...), 0)
	}
	return append(b, 0)
}

func put16(f []byte, off int, v uint16) { binary.LittleEndian.PutUint16(f[off:], v) }
func put32(f []byte, off int, v uint32) { binary.LittleEndian.PutUint32(f[off:], v) }

// rawSMBIOS wraps a table in the RawSMBIOSData header Windows returns.
func rawSMBIOS(entries ...[]byte) []byte {
	var table []byte
	for _, e := range entries {
		table = append(table, e...)
	}
	table = append(table, smbiosEntry(127, 0xFFFF, make([]byte, 4))...)
	raw := []byte{0, 3, 3, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(raw[4:], uint32(len(table)))
	return append(raw, table...)
}

// A self-built desktop like the one this agent was developed on: ASRock board
// with placeholder system strings, one CPU in two sockets' worth of structures,
// one G.Skill stick reported as "Unknown", an empty slot, and a flash array that
// must not count as RAM.
func TestSMBIOSDesktop(t *testing.T) {
	bios := make([]byte, 0x18)
	bios[0x04], bios[0x05], bios[0x08] = 1, 2, 3

	system := make([]byte, 0x1B)
	system[0x04], system[0x05] = 1, 2

	board := make([]byte, 0x0F)
	board[0x04], board[0x05] = 1, 2

	cpu := make([]byte, 0x30)
	cpu[0x18], cpu[0x23], cpu[0x25] = 0x41, 6, 12 // populated, 6 cores, 12 threads
	emptySocket := make([]byte, 0x30)
	emptySocket[0x23], emptySocket[0x25] = 6, 12 // status bit 6 clear: not populated

	sysArray := make([]byte, 0x17)
	sysArray[0x05] = 0x03 // system memory
	flashArray := make([]byte, 0x17)
	flashArray[0x05] = 0x05 // flash

	stick := make([]byte, 0x28)
	put16(stick, 0x04, 0x30) // on the system-memory array
	put16(stick, 0x0C, 16384)
	stick[0x10], stick[0x11], stick[0x17], stick[0x1A] = 1, 2, 3, 4
	stick[0x12] = 0x1A // DDR4
	put16(stick, 0x15, 3200)
	put16(stick, 0x20, 3200)

	emptySlot := make([]byte, 0x28)
	put16(emptySlot, 0x04, 0x30)

	flashDev := make([]byte, 0x28)
	put16(flashDev, 0x04, 0x40)
	put16(flashDev, 0x0C, 16)

	structs := parseRawSMBIOS(rawSMBIOS(
		smbiosEntry(0, 0x00, bios, "American Megatrends Inc.", "P1.60", "01/01/2020"),
		smbiosEntry(1, 0x01, system, "To Be Filled By O.E.M.", "To Be Filled By O.E.M."),
		smbiosEntry(2, 0x02, board, "ASRock", "B450M Pro4-F"),
		smbiosEntry(4, 0x10, cpu),
		smbiosEntry(4, 0x11, emptySocket),
		smbiosEntry(16, 0x30, sysArray),
		smbiosEntry(16, 0x40, flashArray),
		smbiosEntry(17, 0x31, stick, "DIMM 1", "P0 CHANNEL A", "Unknown", "F4-3200C16-16GIS  "),
		smbiosEntry(17, 0x32, emptySlot),
		smbiosEntry(17, 0x41, flashDev),
	))

	b := buildBoardInfo(structs)
	if b == nil || b.Manufacturer != "ASRock" || b.Product != "B450M Pro4-F" || b.BIOS != "P1.60" {
		t.Fatalf("board = %+v", b)
	}
	if b.SystemMaker != "" || b.SystemModel != "" {
		t.Errorf("placeholder system strings leaked: %+v", b)
	}

	if cores, threads := cpuCounts(structs); cores != 6 || threads != 12 {
		t.Errorf("cpu = %d cores / %d threads, want 6/12 (empty socket ignored)", cores, threads)
	}

	m := buildMemoryInfo(structs)
	if m == nil {
		t.Fatal("no memory info")
	}
	if m.TotalBytes != 16<<30 || m.Type != "DDR4" || m.SpeedMTs != 3200 {
		t.Errorf("memory = %d bytes %s @%d, want 16 GiB DDR4 @3200", m.TotalBytes, m.Type, m.SpeedMTs)
	}
	if m.Slots != 2 || len(m.Modules) != 1 {
		t.Fatalf("slots=%d modules=%d, want 2 slots (flash array excluded), 1 module", m.Slots, len(m.Modules))
	}
	mod := m.Modules[0]
	if mod.Manufacturer != "G.Skill" || mod.PartNumber != "F4-3200C16-16GIS" || mod.Slot != "P0 CHANNEL A / DIMM 1" {
		t.Errorf("module = %+v", mod)
	}
}

// Large modules and fast DDR5 overflow the 16-bit size/speed fields into their
// 32-bit extended counterparts.
func TestSMBIOSMemoryExtendedFields(t *testing.T) {
	f := make([]byte, 0x5C)
	put16(f, 0x0C, 0x7FFF)
	put32(f, 0x1C, 32768) // MB
	f[0x12] = 0x22        // DDR5
	put16(f, 0x15, 0xFFFF)
	put32(f, 0x54, 6000)
	put16(f, 0x20, 0xFFFF)
	put32(f, 0x58, 5600)
	f[0x17] = 1
	structs := parseRawSMBIOS(rawSMBIOS(smbiosEntry(17, 1, f, "80CE000080CE")))

	m := buildMemoryInfo(structs)
	if m == nil || len(m.Modules) != 1 {
		t.Fatalf("memory = %+v", m)
	}
	got := m.Modules[0]
	if got.SizeBytes != 32<<30 || got.Type != "DDR5" || got.RatedMTs != 6000 || got.SpeedMTs != 5600 {
		t.Errorf("module = %+v, want 32 GiB DDR5 rated 6000 running 5600", got)
	}
	if got.Manufacturer != "Samsung" {
		t.Errorf("JEDEC 80CE decoded as %q, want Samsung", got.Manufacturer)
	}
}

func TestSMBIOSLaptopModel(t *testing.T) {
	system := make([]byte, 0x1B)
	system[0x04], system[0x05], system[0x06] = 1, 2, 3
	structs := parseRawSMBIOS(rawSMBIOS(
		smbiosEntry(1, 1, system, "LENOVO", "20XW0055MX", "ThinkPad X1 Carbon Gen 9"),
	))
	b := buildBoardInfo(structs)
	if b == nil || b.SystemMaker != "Lenovo" || b.SystemModel != "ThinkPad X1 Carbon Gen 9" {
		t.Fatalf("board = %+v, want Lenovo ThinkPad X1 Carbon Gen 9", b)
	}
}

func TestParseSMBIOSStopsOnTruncation(t *testing.T) {
	board := make([]byte, 0x0F)
	board[0x04] = 1
	table := smbiosEntry(2, 1, board, "ASUSTeK COMPUTER INC.")
	// A structure claiming to be longer than the bytes left must end parsing
	// cleanly, keeping what was already read.
	table = append(table, 17, 0x50, 0, 0)
	got := parseSMBIOS(table)
	if len(got) != 1 || friendlyMaker(got[0].str(0x04)) != "ASUS" {
		t.Fatalf("parsed %d structures (%+v)", len(got), got)
	}
}

func TestMemoryMaker(t *testing.T) {
	for _, c := range []struct{ field, part, want string }{
		{"Unknown", "F4-3200C16-16GIS", "G.Skill"},
		{"80CE", "", "Samsung"},
		{"0000", "CMK32GX4M2E3200C16", "Corsair"},
		{"Kingston", "", "Kingston"},
		{"", "KF432C16BB/16", "Kingston"},
		{"1A2B", "", ""}, // an unknown JEDEC code is not a name
		{"Unknown", "XYZ-123", ""},
	} {
		if got := memoryMaker(c.field, c.part); got != c.want {
			t.Errorf("memoryMaker(%q, %q) = %q, want %q", c.field, c.part, got, c.want)
		}
	}
}

func TestFriendlyMaker(t *testing.T) {
	for in, want := range map[string]string{
		"ASUSTeK COMPUTER INC.":              "ASUS",
		"Micro-Star International Co., Ltd.": "MSI",
		"Gigabyte Technology Co., Ltd.":      "Gigabyte",
		"LENOVO":                             "Lenovo",
		"HP":                                 "HP",
		"Hewlett-Packard":                    "HP",
		"To Be Filled By O.E.M.":             "",
		"Default string":                     "",
		"Framework":                          "Framework",
		"Some Vendor  Ltd":                   "Some Vendor Ltd",
	} {
		if got := friendlyMaker(in); got != want {
			t.Errorf("friendlyMaker(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStorageDescriptorAndDriveNaming(t *testing.T) {
	b := make([]byte, 128)
	binary.LittleEndian.PutUint32(b[12:], 40) // VendorIdOffset
	binary.LittleEndian.PutUint32(b[16:], 48) // ProductIdOffset
	binary.LittleEndian.PutUint32(b[28:], busNVMe)
	copy(b[40:], "NVMe\x00")
	copy(b[48:], "Samsung SSD 970 EVO Plus 1TB    \x00")

	vendor, product, bus, removable, ok := parseStorageDescriptor(b)
	if !ok || removable || bus != busNVMe || vendor != "NVMe" || product != "Samsung SSD 970 EVO Plus 1TB" {
		t.Fatalf("descriptor = %q %q bus=%d removable=%v ok=%v", vendor, product, bus, removable, ok)
	}
	if got := driveModel(vendor, product); got != "Samsung SSD 970 EVO Plus 1TB" {
		t.Errorf("model = %q (generic vendor must not be prefixed)", got)
	}
	if name, internal := busName(bus); name != "NVMe" || !internal {
		t.Errorf("busName = %q internal=%v", name, internal)
	}
	if _, internal := busName(busUSB); internal {
		t.Error("USB drives must not be listed as internal")
	}
	if got := driveModel("SEAGATE", "ST1000NM0033"); got != "SEAGATE ST1000NM0033" {
		t.Errorf("SCSI-style model = %q, want vendor prefixed", got)
	}
	if _, _, _, _, ok := parseStorageDescriptor(make([]byte, 10)); ok {
		t.Error("a short reply must not parse")
	}
}

func TestDriveMaker(t *testing.T) {
	for _, c := range []struct{ vendor, model, want string }{
		{"", "WDC WD10EZEX-08WN4A0", "Western Digital"},
		{"NVMe", "PC SN730 NVMe WDC 512GB", "Western Digital"},
		{"", "WD Blue SN570 1TB", "Western Digital"},
		{"", "ST2000DM008-2FR102", "Seagate"},
		{"", "CT500MX500SSD1", "Crucial"},
		{"", "KINGSTON SA400S37240G", "Kingston"},
		{"NVMe", "INTEL SSDPEKNW010T8", "Intel"},
		{"", "SKHynix_HFS512GD9TNI-L2B0B", "SK hynix"},
		{"", "BC711 NVMe SK hynix 512GB", "SK hynix"},
		{"", "Samsung SSD 870 EVO 1TB", "Samsung"},
		{"", "SAMSUNG MZVLB512HBJQ-000L7", "Samsung"},
		{"", "KBG40ZNS256G NVMe KIOXIA 256GB", "Kioxia"},
		{"SEAGATE", "ST1000NM0033", "Seagate"},
		{"", "Generic Disk 9000", ""},
	} {
		if got := driveMaker(c.vendor, c.model); got != c.want {
			t.Errorf("driveMaker(%q, %q) = %q, want %q", c.vendor, c.model, got, c.want)
		}
	}
}

func TestGPUIdentity(t *testing.T) {
	p, ok := parsePCIInstance(`PCI\VEN_10DE&DEV_1C03&SUBSYS_85AE1043&REV_A1\4&2B8260C3&0&0008`)
	if !ok || p != (pciID{ven: 0x10DE, dev: 0x1C03, subVen: 0x1043, subDev: 0x85AE}) {
		t.Fatalf("parsed %+v ok=%v", p, ok)
	}
	if v, m := gpuVendor(p.ven, ""), gpuMaker(p.ven, p.subVen); v != "NVIDIA" || m != "ASUS" {
		t.Errorf("vendor=%q maker=%q, want NVIDIA / ASUS", v, m)
	}
	// A reference card's subsystem vendor is the chip maker itself: no separate maker.
	p, _ = parsePCIInstance(`PCI\VEN_10DE&DEV_2484&SUBSYS_146B10DE&REV_A1\4&1`)
	if m := gpuMaker(p.ven, p.subVen); m != "" {
		t.Errorf("reference card maker = %q, want empty", m)
	}
	if gpuVendor(0x9999, "Advanced Micro Devices, Inc.") != "AMD" {
		t.Error("unknown vendor id should fall back to the driver's manufacturer")
	}
	if _, ok := parsePCIInstance(`ROOT\BasicDisplay\0000`); ok {
		t.Error("a software display adapter must not parse as PCI")
	}
}

// Modelled on a real fleet PC whose firmware name says "MSI PRO Z690-P" but
// whose AMD chipset devices carry MSI's MS-7B86 (an AM4 B450 board) stamp.
func TestBoardIdentity(t *testing.T) {
	ids := func(instances ...string) []pciID {
		var out []pciID
		for _, s := range instances {
			if p, ok := parsePCIInstance(s); ok {
				out = append(out, p)
			}
		}
		return out
	}
	maker, code := boardIdentity(ids(
		`PCI\VEN_1022&DEV_790B&SUBSYS_7B861462&REV_61\3&11583659&0&A0`, // FCH SMBus
		`PCI\VEN_1022&DEV_43D5&SUBSYS_7B861462&REV_01\4&1`,             // chipset USB
		`PCI\VEN_1022&DEV_149C&SUBSYS_7B861462&REV_00\4&2`,             // CPU USB
		`PCI\VEN_1022&DEV_1480&SUBSYS_14801022&REV_00\3&1`,             // root complex, left at AMD's default
		`PCI\VEN_8086&DEV_F1A8&SUBSYS_390A8086&REV_03\4&3`,             // Intel NVMe SSD: its own stamp
		`PCI\VEN_10DE&DEV_2489&SUBSYS_884F1043&REV_A1\4&4`,             // ASUS-built NVIDIA card
		`PCI\VEN_10DE&DEV_228B&SUBSYS_884F1043&REV_A1\4&5`,             // ...and its HDMI audio
	))
	if maker != "MSI" || code != "MS-7B86" {
		t.Errorf("MSI board = %q %q, want MSI MS-7B86", maker, code)
	}

	maker, code = boardIdentity(ids(
		`PCI\VEN_8086&DEV_7A23&SUBSYS_87471043&REV_11\3&1`,
		`PCI\VEN_8086&DEV_7AE0&SUBSYS_87471043&REV_11\3&2`,
	))
	if maker != "ASUS" || code != "8747" {
		t.Errorf("ASUS board = %q %q, want ASUS 8747", maker, code)
	}

	// One stamped device isn't enough evidence to name the board.
	if maker, code = boardIdentity(ids(`PCI\VEN_1022&DEV_790B&SUBSYS_7B861462&REV_61\3&1`)); code != "" {
		t.Errorf("single device named a board: %q %q", maker, code)
	}
}

func TestOSName(t *testing.T) {
	if got := osName("Windows 10 Pro", "24H2", "26100", 2033); got != "Windows 11 Pro 24H2 (build 26100.2033)" {
		t.Errorf("win11 = %q", got)
	}
	if got := osName("Windows 10 Pro", "22H2", "19045", 6466); got != "Windows 10 Pro 22H2 (build 19045.6466)" {
		t.Errorf("win10 = %q", got)
	}
}

func TestPrimaryIPv4RejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "not a url", "http://"} {
		if ip := primaryIPv4(u); ip != nil {
			t.Errorf("primaryIPv4(%q) = %v, want nil", u, ip)
		}
	}
}
