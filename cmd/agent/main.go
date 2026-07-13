// Command agent is the FabScreenTime Windows logging agent. On non-Windows it
// runs with a stub sampler so the whole pipeline can be exercised on CI/dev
// machines (PLAN.md §4). Silent operation (GUI subsystem, no console/tray) and
// hidden autostart are applied at build/install time, not in this code.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/larsenglund/fabscreentime/internal/agent"
)

// Version is stamped at build time via -ldflags "-X main.Version=...".
var Version = "0.0.0-dev"

func main() {
	server := flag.String("server", "http://localhost:8080", "backend base URL")
	interval := flag.Duration("interval", time.Minute, "sample interval")
	dataDir := flag.String("datadir", defaultDataDir(), "directory for device id + queue")
	once := flag.Bool("once", false, "sample once, print the reading as JSON, and exit (CI smoke test)")
	flag.Parse()

	if *once {
		r, err := agent.NewSampler().Sample()
		if err != nil {
			log.Fatalf("sample: %v", err)
		}
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
		return
	}

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatalf("datadir: %v", err)
	}
	deviceUUID, err := loadOrCreateDeviceID(filepath.Join(*dataDir, "device_id"))
	if err != nil {
		log.Fatalf("device id: %v", err)
	}
	hostname, _ := os.Hostname()

	queue, err := agent.NewQueue(filepath.Join(*dataDir, "queue.json"), 0)
	if err != nil {
		log.Fatalf("queue: %v", err)
	}

	a := agent.New(agent.Config{
		DeviceUUID:   deviceUUID,
		Hostname:     hostname,
		AgentVersion: Version,
		Interval:     *interval,
	}, agent.NewSampler(), queue, agent.NewHTTPUploader(*server))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("agent %s starting: device=%s host=%s server=%s interval=%s",
		Version, deviceUUID, hostname, *server, *interval)
	a.Run(ctx)
}

func loadOrCreateDeviceID(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 8 {
		return string(b), nil
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

func defaultDataDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "FabScreenTime")
	}
	return ".fabscreentime"
}
