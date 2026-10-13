//go:build !windows

package agent

import (
	"net"
	"runtime"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

// The stub inventory reports only what is portable, so the full pipeline (agent
// → ingest → dashboard card) can be exercised on Linux/CI without a Windows box.
func collectPlatform(info *shared.SystemInfo, ip net.IP) {
	info.OS = runtime.GOOS + "/" + runtime.GOARCH
	info.CPU = &shared.CPUInfo{Model: "Development machine", Threads: runtime.NumCPU()}
	if ip != nil {
		info.Network = &shared.NetworkInfo{IPv4: ip.String()}
	}
}
