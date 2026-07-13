// Package update implements the signed-manifest verification that separates
// "auto-updating tracker" from "botnet C2" (PLAN.md §5.3). A compromised backend
// can serve any bytes it likes; the agent accepts an update only if the manifest
// is signed by a key pinned in the agent binary, binds a strictly newer build,
// and points at a same-origin URL — all checked BEFORE anything is downloaded or
// swapped (fail-closed).
package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

var (
	// ErrBadSignature means no pinned key verified the manifest.
	ErrBadSignature = errors.New("update: manifest signature not valid for any pinned key")
	// ErrNotNewer blocks replay/downgrade of an older (still validly signed) build.
	ErrNotNewer = errors.New("update: manifest build is not newer than current (downgrade blocked)")
	// ErrBadURL blocks a compromised backend redirecting the download off-origin.
	ErrBadURL = errors.New("update: download url is not same-origin (server-relative)")
	// ErrHashMismatch means the downloaded bytes don't match the signed hash.
	ErrHashMismatch = errors.New("update: downloaded binary sha256 mismatch")
	// ErrNoPinnedKeys means the agent has no keys compiled in — fail closed.
	ErrNoPinnedKeys = errors.New("update: no pinned keys configured")
)

// CanonicalBytes is the exact byte sequence covered by the signature. Go marshals
// struct fields in declaration order with no floats/maps here, so this is
// deterministic across builds — the property the whole scheme depends on.
func CanonicalBytes(m shared.Manifest) []byte {
	b, _ := json.Marshal(m)
	return b
}

// Sign produces a SignedManifest using an offline private key.
func Sign(m shared.Manifest, priv ed25519.PrivateKey) shared.SignedManifest {
	sig := ed25519.Sign(priv, CanonicalBytes(m))
	return shared.SignedManifest{Manifest: m, Sig: hex.EncodeToString(sig)}
}

// Verify checks the signature against every pinned key (primary + rotation), so
// a primary-key compromise is recoverable by signing with the backup (PLAN.md §5.3 H2).
func Verify(sm shared.SignedManifest, pinned []ed25519.PublicKey) error {
	if len(pinned) == 0 {
		return ErrNoPinnedKeys
	}
	sig, err := hex.DecodeString(sm.Sig)
	if err != nil {
		return ErrBadSignature
	}
	msg := CanonicalBytes(sm.Manifest)
	for _, pk := range pinned {
		if len(pk) == ed25519.PublicKeySize && ed25519.Verify(pk, msg, sig) {
			return nil
		}
	}
	return ErrBadSignature
}

// ShouldApply returns nil only if the manifest is genuinely safe to apply:
// signed by a pinned key, strictly newer than currentBuild, and same-origin.
// Callers must treat any non-nil error as "keep running the current version".
func ShouldApply(sm shared.SignedManifest, currentBuild int64, pinned []ed25519.PublicKey) error {
	if err := Verify(sm, pinned); err != nil {
		return err
	}
	if sm.Manifest.Build <= currentBuild {
		return ErrNotNewer
	}
	if !IsSameOrigin(sm.Manifest.URL) {
		return ErrBadURL
	}
	return nil
}

// IsSameOrigin accepts only a server-relative path ("/agent/…") and rejects
// absolute URLs, scheme-relative ("//host"), and empty values.
func IsSameOrigin(u string) bool {
	if !strings.HasPrefix(u, "/") || strings.HasPrefix(u, "//") {
		return false
	}
	return true
}

// VerifyBytes confirms downloaded bytes match the signed hash, before any swap.
func VerifyBytes(wantHexSHA256 string, data []byte) error {
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), wantHexSHA256) {
		return ErrHashMismatch
	}
	return nil
}
