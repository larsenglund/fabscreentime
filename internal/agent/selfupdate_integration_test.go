package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/larsenglund/fabscreentime/internal/shared"
	"github.com/larsenglund/fabscreentime/internal/update"
)

// This exercises the whole Phase 1 acceptance list (PLAN.md §10) end to end,
// in-process: a signed v2 is applied, and tampered / unsigned / downgrade /
// off-origin / wrong-hash manifests are all rejected BEFORE the binary is
// touched.
func TestSelfUpdateEndToEnd(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	rotationPub, _, _ := ed25519.GenerateKey(rand.Reader)
	pinned := []ed25519.PublicKey{pub, rotationPub}

	newBinary := []byte("this is agent v2\x00\x01\x02")
	sum := sha256.Sum256(newBinary)
	served := newBinary // what the fake backend actually serves

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agent/download" {
			_, _ = w.Write(served)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	baseManifest := func() shared.Manifest {
		return shared.Manifest{
			Version:   "2.0.0",
			Build:     2,
			SHA256:    hex.EncodeToString(sum[:]),
			Timestamp: 1_700_000_000,
			URL:       "/agent/download",
		}
	}

	newUpdater := func(currentBuild int64) *SelfUpdater {
		return NewSelfUpdater(srv.URL, currentBuild, 0, pinned)
	}

	// Fresh temp "exe" per case.
	writeExe := func(t *testing.T) string {
		p := filepath.Join(t.TempDir(), "agent.bin")
		if err := os.WriteFile(p, []byte("agent v1"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	assertUntouched := func(t *testing.T, exe string) {
		got, _ := os.ReadFile(exe)
		if string(got) != "agent v1" {
			t.Fatalf("binary was modified on a rejected update: %q", got)
		}
	}

	t.Run("valid v2 is applied", func(t *testing.T) {
		exe := writeExe(t)
		err := newUpdater(1).prepare(context.Background(), update.Sign(baseManifest(), priv), exe)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		got, _ := os.ReadFile(exe)
		if string(got) != string(newBinary) {
			t.Fatalf("binary not updated to v2")
		}
	})

	t.Run("tampered manifest rejected", func(t *testing.T) {
		exe := writeExe(t)
		sm := update.Sign(baseManifest(), priv)
		sm.Manifest.Version = "6.6.6" // change a signed field after signing
		if err := newUpdater(1).prepare(context.Background(), sm, exe); err != update.ErrBadSignature {
			t.Fatalf("want ErrBadSignature, got %v", err)
		}
		assertUntouched(t, exe)
	})

	t.Run("unsigned (empty sig) rejected", func(t *testing.T) {
		exe := writeExe(t)
		sm := shared.SignedManifest{Manifest: baseManifest(), Sig: ""}
		if err := newUpdater(1).prepare(context.Background(), sm, exe); err != update.ErrBadSignature {
			t.Fatalf("want ErrBadSignature, got %v", err)
		}
		assertUntouched(t, exe)
	})

	t.Run("downgrade rejected", func(t *testing.T) {
		exe := writeExe(t)
		// current build 5 > manifest build 2
		if err := newUpdater(5).prepare(context.Background(), update.Sign(baseManifest(), priv), exe); err != update.ErrNotNewer {
			t.Fatalf("want ErrNotNewer, got %v", err)
		}
		assertUntouched(t, exe)
	})

	t.Run("off-origin url rejected", func(t *testing.T) {
		exe := writeExe(t)
		m := baseManifest()
		m.URL = "https://evil.example/agent.exe"
		if err := newUpdater(1).prepare(context.Background(), update.Sign(m, priv), exe); err != update.ErrBadURL {
			t.Fatalf("want ErrBadURL, got %v", err)
		}
		assertUntouched(t, exe)
	})

	t.Run("wrong-hash payload rejected", func(t *testing.T) {
		exe := writeExe(t)
		served = []byte("malicious different bytes") // backend serves something else
		defer func() { served = newBinary }()
		if err := newUpdater(1).prepare(context.Background(), update.Sign(baseManifest(), priv), exe); err != update.ErrHashMismatch {
			t.Fatalf("want ErrHashMismatch, got %v", err)
		}
		assertUntouched(t, exe)
	})
}
