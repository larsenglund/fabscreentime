package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

func TestSystemInfoStoredAndServed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv, _ := newTestServer(t, now)
	h := srv.Handler()
	uuid, token := enrollDevice(t, h, "desktop")

	// Before any report: present device, no inventory.
	var got shared.DeviceSystemInfo
	rec := doJSON(t, h, http.MethodGet, "/api/devices/"+uuid+"/system", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	mustJSON(t, rec.Body.Bytes(), &got)
	if got.SystemInfo != nil || got.UpdatedAt != 0 {
		t.Fatalf("fresh device: got %+v, want no inventory", got)
	}

	sample := []shared.Sample{{ClientTS: now.Unix(), MonitorOn: 1, Exe: "a.exe"}}
	doIngest(t, h, token, shared.IngestRequest{
		Samples: sample,
		SystemInfo: &shared.SystemInfo{
			CPU:    &shared.CPUInfo{Model: "AMD Ryzen 5 2600 Six-Core Processor", Vendor: "AMD", Cores: 6, Threads: 12},
			Board:  &shared.BoardInfo{Manufacturer: "MSI", Product: "PRO Z690-P DDR4 (MS-7D36)", HWMaker: "MSI", HWCode: "MS-7B86"},
			Drives: []shared.DriveInfo{{Model: "INTEL SSDPEKNW010T8", Maker: "Intel", SizeBytes: 1024209543168, Bus: "NVMe", Media: "SSD"}},
		},
	})
	mustJSON(t, doJSON(t, h, http.MethodGet, "/api/devices/"+uuid+"/system", "", nil).Body.Bytes(), &got)
	if got.SystemInfo == nil || got.SystemInfo.CPU == nil || got.SystemInfo.CPU.Cores != 6 {
		t.Fatalf("inventory not stored: %+v", got.SystemInfo)
	}
	if len(got.SystemInfo.Drives) != 1 || got.SystemInfo.Drives[0].Bus != "NVMe" {
		t.Fatalf("drives = %+v", got.SystemInfo.Drives)
	}
	if b := got.SystemInfo.Board; b == nil || b.HWMaker != "MSI" || b.HWCode != "MS-7B86" {
		t.Fatalf("board hardware identity lost on the way through: %+v", b)
	}
	if got.UpdatedAt != now.Unix() {
		t.Fatalf("updated_at = %d, want %d", got.UpdatedAt, now.Unix())
	}

	// Most ingests carry no inventory; that must not erase the stored one.
	doIngest(t, h, token, shared.IngestRequest{
		Samples: []shared.Sample{{ClientTS: now.Unix() + 60, MonitorOn: 1, Exe: "a.exe"}},
	})
	mustJSON(t, doJSON(t, h, http.MethodGet, "/api/devices/"+uuid+"/system", "", nil).Body.Bytes(), &got)
	if got.SystemInfo == nil || got.SystemInfo.CPU == nil {
		t.Fatal("an ingest without system_info wiped the stored inventory")
	}

	if rec := doJSON(t, h, http.MethodGet, "/api/devices/nope/system", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown device status = %d, want 404", rec.Code)
	}
}

func TestSanitizeSystemInfoBoundsInput(t *testing.T) {
	long := strings.Repeat("é", 500) // multi-byte, so clipping must respect rune boundaries
	in := &shared.SystemInfo{
		OS:  long,
		CPU: &shared.CPUInfo{Model: long, Cores: -4},
	}
	for i := 0; i < 100; i++ {
		in.Drives = append(in.Drives, shared.DriveInfo{Model: "d"})
	}
	out := sanitizeSystemInfo(in)
	if len(out.OS) > maxInfoString || len(out.CPU.Model) > maxInfoString {
		t.Fatalf("strings not clipped: os=%d cpu=%d bytes", len(out.OS), len(out.CPU.Model))
	}
	if !strings.HasPrefix(long, out.OS) {
		t.Fatal("clipping split a multi-byte character")
	}
	if out.CPU.Cores != 0 {
		t.Fatalf("negative cores kept: %d", out.CPU.Cores)
	}
	if len(out.Drives) != maxInfoDrives {
		t.Fatalf("drives = %d, want capped at %d", len(out.Drives), maxInfoDrives)
	}
}
