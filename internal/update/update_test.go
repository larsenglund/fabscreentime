package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

func keypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return pub, priv
}

func manifest() shared.Manifest {
	return shared.Manifest{
		Version:   "1.2.0",
		Build:     5,
		SHA256:    "abcd",
		Timestamp: 1_700_000_000,
		URL:       "/agent/download?v=1.2.0",
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv := keypair(t)
	sm := Sign(manifest(), priv)
	if err := Verify(sm, []ed25519.PublicKey{pub}); err != nil {
		t.Fatalf("verify valid: %v", err)
	}
}

func TestVerifyRejectsTamperedManifest(t *testing.T) {
	pub, priv := keypair(t)
	sm := Sign(manifest(), priv)
	sm.Manifest.Build = 999 // tamper after signing
	if err := Verify(sm, []ed25519.PublicKey{pub}); err != ErrBadSignature {
		t.Fatalf("want ErrBadSignature for tampered manifest, got %v", err)
	}
}

func TestVerifyRejectsWrongKeyAcceptsRotationKey(t *testing.T) {
	pubPrimary, privPrimary := keypair(t)
	pubRotation, _ := keypair(t)
	pubAttacker, privAttacker := keypair(t)

	sm := Sign(manifest(), privPrimary)
	// Pinned set is {primary, rotation}; primary must verify.
	if err := Verify(sm, []ed25519.PublicKey{pubRotation, pubPrimary}); err != nil {
		t.Fatalf("primary key should verify against pinned set: %v", err)
	}
	// A manifest signed by an unpinned attacker key is rejected.
	smBad := Sign(manifest(), privAttacker)
	if err := Verify(smBad, []ed25519.PublicKey{pubPrimary, pubRotation}); err != ErrBadSignature {
		t.Fatalf("want ErrBadSignature for attacker key, got %v", err)
	}
	_ = pubAttacker
}

func TestVerifyNoPinnedKeysFailsClosed(t *testing.T) {
	_, priv := keypair(t)
	sm := Sign(manifest(), priv)
	if err := Verify(sm, nil); err != ErrNoPinnedKeys {
		t.Fatalf("want ErrNoPinnedKeys, got %v", err)
	}
}

func TestShouldApply(t *testing.T) {
	pub, priv := keypair(t)
	pinned := []ed25519.PublicKey{pub}

	t.Run("newer build applies", func(t *testing.T) {
		if err := ShouldApply(Sign(manifest(), priv), 4, pinned); err != nil {
			t.Fatalf("want apply, got %v", err)
		}
	})
	t.Run("equal build blocked", func(t *testing.T) {
		if err := ShouldApply(Sign(manifest(), priv), 5, pinned); err != ErrNotNewer {
			t.Fatalf("want ErrNotNewer, got %v", err)
		}
	})
	t.Run("older build blocked (downgrade)", func(t *testing.T) {
		if err := ShouldApply(Sign(manifest(), priv), 9, pinned); err != ErrNotNewer {
			t.Fatalf("want ErrNotNewer, got %v", err)
		}
	})
	t.Run("bad signature blocked before anything", func(t *testing.T) {
		sm := Sign(manifest(), priv)
		sm.Sig = "00"
		if err := ShouldApply(sm, 4, pinned); err != ErrBadSignature {
			t.Fatalf("want ErrBadSignature, got %v", err)
		}
	})
	t.Run("off-origin url blocked", func(t *testing.T) {
		m := manifest()
		m.URL = "https://evil.example/agent.exe"
		if err := ShouldApply(Sign(m, priv), 4, pinned); err != ErrBadURL {
			t.Fatalf("want ErrBadURL, got %v", err)
		}
	})
	t.Run("scheme-relative url blocked", func(t *testing.T) {
		m := manifest()
		m.URL = "//evil.example/agent.exe"
		if err := ShouldApply(Sign(m, priv), 4, pinned); err != ErrBadURL {
			t.Fatalf("want ErrBadURL, got %v", err)
		}
	})
}

func TestVerifyBytes(t *testing.T) {
	data := []byte("agent binary bytes")
	sum := sha256.Sum256(data)
	good := hex.EncodeToString(sum[:])
	if err := VerifyBytes(good, data); err != nil {
		t.Fatalf("want match, got %v", err)
	}
	if err := VerifyBytes(strings.ToUpper(good), data); err != nil {
		t.Fatalf("hex compare should be case-insensitive: %v", err)
	}
	if err := VerifyBytes("deadbeef", data); err != ErrHashMismatch {
		t.Fatalf("want ErrHashMismatch, got %v", err)
	}
}
