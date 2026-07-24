package agent

import (
	"crypto/ed25519"
	"encoding/hex"
	"log"
)

// pinnedUpdateKeysHex holds the update-signing PUBLIC keys compiled into the
// agent (a primary plus an optional offline rotation key — PLAN.md §5.3 H2). A
// compromised backend cannot forge an update without the matching offline
// private key, which never leaves your hardware token / USB key.
//
// Generate a keypair with `fst-sign genkey` and paste the printed public key
// here. Empty by default so a stock build FAILS CLOSED — it will never
// self-update until you pin your own key.
var pinnedUpdateKeysHex = []string{
	"0a8065a7229b292842b055aed80c06b338ada7becff73453d23f2ef7cdcfff53", // primary (release.key on Lars's dev machine)
	// "….", // rotation — generate a second offline key and pin it before real deployment (§5.3 H2)
}

// PinnedUpdateKeys returns the parsed pinned public keys (invalid entries skipped).
func PinnedUpdateKeys() []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, h := range pinnedUpdateKeysHex {
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != ed25519.PublicKeySize {
			log.Printf("pinned update key invalid, skipping")
			continue
		}
		out = append(out, ed25519.PublicKey(b))
	}
	return out
}
