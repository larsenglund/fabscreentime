package agent

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/larsenglund/fabscreentime/internal/shared"
	"github.com/larsenglund/fabscreentime/internal/update"
)

// Updater decides on and applies self-updates. Abstracted so the core loop and
// its tests never perform real downloads or relaunch the process.
type Updater interface {
	Maybe(ctx context.Context, sm shared.SignedManifest)
}

// SelfUpdater implements the signed, fail-closed self-update flow (PLAN.md §5).
// Order is: verify signature + monotonic + same-origin → download → verify hash
// → atomic rename-swap → relaunch. Nothing touches the running binary until the
// downloaded bytes are proven to match a pinned-key-signed manifest.
type SelfUpdater struct {
	BaseURL      string
	CurrentBuild int64
	Pinned       []ed25519.PublicKey
	Client       *http.Client
}

// NewSelfUpdater builds a SelfUpdater.
func NewSelfUpdater(baseURL string, currentBuild int64, pinned []ed25519.PublicKey) *SelfUpdater {
	return &SelfUpdater{
		BaseURL:      baseURL,
		CurrentBuild: currentBuild,
		Pinned:       pinned,
		Client:       &http.Client{Timeout: 5 * time.Minute},
	}
}

// Maybe verifies and, if safe, applies the update, then relaunches. On any
// failure it logs and returns, leaving the current version running.
func (u *SelfUpdater) Maybe(ctx context.Context, sm shared.SignedManifest) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("update: locate self: %v", err)
		return
	}
	if err := u.prepare(ctx, sm, exe); err != nil {
		if err != update.ErrNotNewer { // "not newer" is the normal steady state, not worth logging
			log.Printf("update: %v", err)
		}
		return
	}
	log.Printf("update applied; relaunching as %s (build %d)", sm.Manifest.Version, sm.Manifest.Build)
	relaunch(exe)
}

// prepare runs the whole fail-closed pipeline — verify signature + monotonic +
// same-origin, download, verify hash, then atomic swap — and swaps the binary at
// exe. It does NOT relaunch, so the full chain is unit-testable in-process.
// Every check happens before the running binary is touched.
func (u *SelfUpdater) prepare(ctx context.Context, sm shared.SignedManifest, exe string) error {
	if err := update.ShouldApply(sm, u.CurrentBuild, u.Pinned); err != nil {
		return err
	}
	data, err := u.download(ctx, sm.Manifest.URL)
	if err != nil {
		return err
	}
	if err := update.VerifyBytes(sm.Manifest.SHA256, data); err != nil {
		return err
	}
	return applyUpdateAt(exe, data)
}

func (u *SelfUpdater) download(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 128<<20)) // 128 MiB safety cap
}

// applyUpdateAt performs the Windows-safe rename-swap: rename the running exe to
// .old (permitted while running), write the new bytes, and roll back on failure.
// Path-parameterized so it is unit-testable without touching the live binary.
func applyUpdateAt(exe string, newBytes []byte) error {
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.WriteFile(exe, newBytes, 0o755); err != nil {
		_ = os.Rename(old, exe) // roll back so a working agent survives
		return err
	}
	return nil
}

// relaunch starts the freshly written binary and exits. Real-time AV can briefly
// lock the new exe, so retry with backoff before giving up (PLAN.md §5.2).
func relaunch(exe string) {
	var lastErr error
	for _, d := range []time.Duration{0, 500 * time.Millisecond, 2 * time.Second} {
		if d > 0 {
			time.Sleep(d)
		}
		cmd := exec.Command(exe, os.Args[1:]...)
		if err := cmd.Start(); err != nil {
			lastErr = err
			continue
		}
		os.Exit(0)
	}
	log.Printf("relaunch failed after retries: %v (staying on current process)", lastErr)
}
