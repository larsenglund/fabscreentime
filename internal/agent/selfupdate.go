package agent

import (
	"context"
	"crypto/ed25519"
	"errors"
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

// ErrQuarantined blocks re-applying a build that already crash-looped here (§5.4).
var ErrQuarantined = errors.New("update: build is quarantined (crash-looped here)")

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
	Quarantined  int64 // a build that crash-looped here; refuse to re-apply it (§5.4)
	Client       *http.Client
}

// NewSelfUpdater builds a SelfUpdater. quarantined is a build number that already
// crash-looped on this machine and must not be applied again (0 = none).
func NewSelfUpdater(baseURL string, currentBuild, quarantined int64, pinned []ed25519.PublicKey) *SelfUpdater {
	return &SelfUpdater{
		BaseURL:      baseURL,
		CurrentBuild: currentBuild,
		Quarantined:  quarantined,
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
		// "not newer" and "quarantined" are normal steady states, not worth logging.
		if err != update.ErrNotNewer && err != ErrQuarantined {
			log.Printf("update: %v", err)
		}
		return
	}
	log.Printf("update applied; relaunching as %s (build %d)", sm.Manifest.Version, sm.Manifest.Build)
	Relaunch(exe)
}

// prepare runs the whole fail-closed pipeline — verify signature + monotonic +
// same-origin, download, verify hash, then atomic swap — and swaps the binary at
// exe. It does NOT relaunch, so the full chain is unit-testable in-process.
// Every check happens before the running binary is touched.
func (u *SelfUpdater) prepare(ctx context.Context, sm shared.SignedManifest, exe string) error {
	// Refuse a build that already crash-looped here — otherwise the restored old
	// build would immediately re-apply the same bad update the server still
	// advertises (auto-rollback quarantine, §5.4).
	if u.Quarantined != 0 && sm.Manifest.Build == u.Quarantined {
		return ErrQuarantined
	}
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

// Relaunch starts the binary at exe with the current args and exits the running
// process. Real-time AV can briefly lock a freshly-written exe, so it retries
// with backoff before giving up (PLAN.md §5.2). Used by the updater and by the
// startup rollback path.
func Relaunch(exe string) {
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
