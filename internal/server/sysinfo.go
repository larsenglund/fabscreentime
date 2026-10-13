package server

import (
	"strings"
	"unicode/utf8"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

// Bounds for a stored inventory. Generous for any real PC; they exist so a
// buggy or hostile agent can't park megabytes of text on the dashboard.
const (
	maxInfoString  = 160
	maxInfoGPUs    = 8
	maxInfoDrives  = 24
	maxInfoModules = 32
)

// sanitizeSystemInfo returns a bounded copy of an agent-reported inventory:
// strings clipped, lists capped, negative numbers zeroed.
func sanitizeSystemInfo(in *shared.SystemInfo) *shared.SystemInfo {
	out := &shared.SystemInfo{CollectedAt: in.CollectedAt, OS: clip(in.OS)}
	if c := in.CPU; c != nil {
		out.CPU = &shared.CPUInfo{Model: clip(c.Model), Vendor: clip(c.Vendor), Cores: nonNeg(c.Cores), Threads: nonNeg(c.Threads)}
	}
	if m := in.Memory; m != nil {
		mm := &shared.MemoryInfo{TotalBytes: m.TotalBytes, Type: clip(m.Type), SpeedMTs: nonNeg(m.SpeedMTs), Slots: nonNeg(m.Slots)}
		for i, mod := range m.Modules {
			if i == maxInfoModules {
				break
			}
			mm.Modules = append(mm.Modules, shared.MemoryModule{
				Slot: clip(mod.Slot), SizeBytes: mod.SizeBytes, Type: clip(mod.Type),
				SpeedMTs: nonNeg(mod.SpeedMTs), RatedMTs: nonNeg(mod.RatedMTs),
				Manufacturer: clip(mod.Manufacturer), PartNumber: clip(mod.PartNumber),
			})
		}
		out.Memory = mm
	}
	for i, g := range in.GPUs {
		if i == maxInfoGPUs {
			break
		}
		out.GPUs = append(out.GPUs, shared.GPUInfo{Model: clip(g.Model), Vendor: clip(g.Vendor), Maker: clip(g.Maker), VRAMBytes: g.VRAMBytes})
	}
	if b := in.Board; b != nil {
		out.Board = &shared.BoardInfo{
			Manufacturer: clip(b.Manufacturer), Product: clip(b.Product),
			SystemMaker: clip(b.SystemMaker), SystemModel: clip(b.SystemModel), BIOS: clip(b.BIOS),
			HWMaker: clip(b.HWMaker), HWCode: clip(b.HWCode),
		}
	}
	for i, d := range in.Drives {
		if i == maxInfoDrives {
			break
		}
		out.Drives = append(out.Drives, shared.DriveInfo{
			Model: clip(d.Model), Maker: clip(d.Maker), SizeBytes: d.SizeBytes, Bus: clip(d.Bus), Media: clip(d.Media),
		})
	}
	if n := in.Network; n != nil {
		out.Network = &shared.NetworkInfo{
			Name: clip(n.Name), Kind: clip(n.Kind), Adapter: clip(n.Adapter), IPv4: clip(n.IPv4), LinkMbps: nonNeg(n.LinkMbps),
		}
	}
	return out
}

// clip trims s and cuts it to maxInfoString bytes on a rune boundary.
func clip(s string) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	if len(s) <= maxInfoString {
		return s
	}
	s = s[:maxInfoString]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func nonNeg(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
