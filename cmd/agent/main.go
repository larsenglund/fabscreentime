// Command agent is the FabScreenTime Windows logging agent. On non-Windows it
// runs with a stub sampler so the whole pipeline can be exercised on CI/dev
// machines (PLAN.md §4). Silent operation (GUI subsystem, no console/tray) and
// hidden autostart are applied at build/install time, not in this code.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/larsenglund/fabscreentime/internal/agent"
)

// Version and Build are stamped at build time via
// -ldflags "-X main.Version=1.2.0 -X main.Build=2". Build is the monotonic
// number the updater compares; it must increase every release.
var (
	Version = "0.0.0-dev"
	Build   = "0"
)

const defaultServer = "http://localhost:8080"

func main() {
	server := flag.String("server", defaultServer, "backend base URL")
	interval := flag.Duration("interval", time.Minute, "sample interval")
	dataDir := flag.String("datadir", agent.InstallDir(), "directory for credentials + queue")
	enroll := flag.String("enroll", "", "one-time enrollment secret (dev/manual; the installer uses enroll.json)")
	once := flag.Bool("once", false, "sample once, print the reading as JSON, and exit (CI smoke test)")
	install := flag.Bool("install", false, "install silent autostart (hidden logon Scheduled Task) and exit")
	uninstall := flag.Bool("uninstall", false, "remove the autostart Scheduled Task and exit")
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

	if *install {
		if err := agent.Install(*server); err != nil {
			log.Fatalf("install: %v", err)
		}
		log.Printf("installed and started; reporting to %s", *server)
		return
	}
	if *uninstall {
		if err := agent.Uninstall(); err != nil {
			log.Fatalf("uninstall: %v", err)
		}
		log.Print("uninstalled autostart task")
		return
	}

	// One agent per user session — the update relaunch and a Task Scheduler
	// trigger must not double-run (PLAN.md §5.2).
	release, ok := agent.SingleInstance("FabScreenTimeAgent")
	if !ok {
		log.Print("another agent instance is already running; exiting")
		return
	}
	defer release()

	build, _ := strconv.ParseInt(Build, 10, 64)

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatalf("datadir: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverURL, creds, err := resolveCredentials(ctx, *server, *dataDir, *enroll)
	if err != nil {
		log.Fatalf("credentials: %v", err)
	}
	hostname, _ := os.Hostname()

	queue, err := agent.NewQueue(fpJoin(*dataDir, "queue.json"), 0)
	if err != nil {
		log.Fatalf("queue: %v", err)
	}

	// Self-update is enabled only if keys are pinned (fail-closed, PLAN.md §5.3).
	var updater agent.Updater
	if keys := agent.PinnedUpdateKeys(); len(keys) > 0 {
		updater = agent.NewSelfUpdater(serverURL, build, keys)
	} else {
		log.Print("no pinned update keys compiled in — self-update disabled")
	}

	a := agent.New(agent.Config{
		DeviceUUID:   creds.DeviceUUID,
		Hostname:     hostname,
		AgentVersion: Version,
		Build:        build,
		Interval:     *interval,
	}, agent.NewSampler(), queue, agent.NewHTTPUploader(serverURL, creds.APIToken), updater)

	log.Printf("agent %s (build %d) starting: device=%s host=%s server=%s interval=%s",
		Version, build, creds.DeviceUUID, hostname, serverURL, *interval)
	a.Run(ctx)
}

// resolveCredentials returns the server URL and durable credentials, enrolling on
// first run if a one-time secret is available (from -enroll or the installer's
// enroll.json). Returns an error only when the agent cannot obtain an identity.
func resolveCredentials(ctx context.Context, serverFlag, dataDir, enrollFlag string) (string, *agent.Credentials, error) {
	if creds, err := agent.LoadCredentials(dataDir); err != nil {
		return "", nil, fmt.Errorf("load credentials: %w", err)
	} else if creds != nil {
		return serverFlag, creds, nil
	}

	// Not enrolled yet: find a one-time secret. The -enroll flag wins; otherwise
	// read the installer's enroll.json sidecar.
	secret, server := enrollFlag, serverFlag
	if secret == "" {
		if cfg, err := agent.ReadEnrollFile(dataDir); err != nil {
			return "", nil, fmt.Errorf("read enroll.json: %w", err)
		} else if cfg != nil {
			secret = cfg.EnrollSecret
			// A manual double-click may not pass -server; take it from the sidecar.
			if serverFlag == defaultServer && cfg.Server != "" {
				server = cfg.Server
			}
		}
	}
	if secret == "" {
		return "", nil, fmt.Errorf("no credentials and no enrollment secret; add this device from the dashboard")
	}

	creds, err := enrollWithRetry(ctx, server, secret)
	if err != nil {
		return "", nil, err
	}
	if err := agent.SaveCredentials(dataDir, creds); err != nil {
		return "", nil, fmt.Errorf("save credentials: %w", err)
	}
	agent.RemoveEnrollFile(dataDir)
	log.Printf("enrolled as device %s", creds.DeviceUUID)
	return server, creds, nil
}

// enrollWithRetry retries transient enrollment failures with backoff. A rejected
// secret (expired/used/unknown) is terminal and returned immediately.
func enrollWithRetry(ctx context.Context, server, secret string) (*agent.Credentials, error) {
	backoff := []time.Duration{0, 2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second}
	var lastErr error
	for _, d := range backoff {
		if d > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(d):
			}
		}
		creds, err := agent.Enroll(ctx, server, secret, hostnameOrEmpty())
		if err == nil {
			return creds, nil
		}
		if err == agent.ErrEnrollRejected {
			return nil, fmt.Errorf("enrollment rejected (secret expired, already used, or unknown)")
		}
		lastErr = err
		log.Printf("enroll attempt failed, will retry: %v", err)
	}
	return nil, fmt.Errorf("enrollment failed after retries: %w", lastErr)
}

func hostnameOrEmpty() string {
	h, _ := os.Hostname()
	return h
}

// fpJoin avoids importing path/filepath just for one call site.
func fpJoin(dir, name string) string { return dir + string(os.PathSeparator) + name }
