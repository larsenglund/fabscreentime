// Command fst-sign is the offline release-signing tool (PLAN.md §5.3, §8.5).
// The private key it generates must live OFFLINE (a hardware token or a USB key
// kept off the Proxmox server); only the public key is pinned in the agent. A
// compromised backend that never has the private key cannot forge an update.
//
//	fst-sign genkey  -out release.key
//	    → writes the private key (0600) and prints the public key to pin.
//
//	fst-sign sign    -key release.key -in dist/agent.exe -version 1.2.0 -build 2
//	    → prints/writes a signed manifest.json for that binary.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/larsenglund/fabscreentime/internal/shared"
	"github.com/larsenglund/fabscreentime/internal/update"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "genkey":
		genkey(os.Args[2:])
	case "sign":
		sign(os.Args[2:])
	case "verify":
		verify(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: fst-sign <genkey|sign|verify> [flags]")
	os.Exit(2)
}

// verify checks a signed manifest against a pinned public key and confirms the
// binary matches — the out-of-band first-install check (PLAN.md §5.3 H1).
func verify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	pubHex := fs.String("pub", "", "pinned public key (hex)")
	manPath := fs.String("manifest", "manifest.json", "signed manifest path")
	binPath := fs.String("bin", "", "agent binary to check against the manifest")
	_ = fs.Parse(args)

	pubRaw, err := hex.DecodeString(*pubHex)
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		fmt.Fprintln(os.Stderr, "verify: -pub must be a valid ed25519 public key (hex)")
		os.Exit(2)
	}
	data, err := os.ReadFile(*manPath)
	if err != nil {
		fatal(err)
	}
	var sm shared.SignedManifest
	if err := json.Unmarshal(data, &sm); err != nil {
		fatal(err)
	}
	if err := update.Verify(sm, []ed25519.PublicKey{pubRaw}); err != nil {
		fmt.Fprintln(os.Stderr, "SIGNATURE INVALID:", err)
		os.Exit(1)
	}
	if *binPath != "" {
		bin, err := os.ReadFile(*binPath)
		if err != nil {
			fatal(err)
		}
		if err := update.VerifyBytes(sm.Manifest.SHA256, bin); err != nil {
			fmt.Fprintln(os.Stderr, "BINARY MISMATCH:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("OK: %s build %d, signature valid, hash matches\n", sm.Manifest.Version, sm.Manifest.Build)
}

func genkey(args []string) {
	fs := flag.NewFlagSet("genkey", flag.ExitOnError)
	out := fs.String("out", "release.key", "path to write the private key (kept OFFLINE)")
	_ = fs.Parse(args)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*out, []byte(hex.EncodeToString(priv)), 0o600); err != nil {
		fatal(err)
	}
	fmt.Printf("Private key written to %s (keep it offline; never commit it).\n\n", *out)
	fmt.Println("Pin this public key in internal/agent/pinnedkeys.go:")
	fmt.Printf("  %q\n", hex.EncodeToString(pub))
}

func sign(args []string) {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyPath := fs.String("key", "release.key", "path to the private key")
	in := fs.String("in", "", "path to the agent binary to sign")
	version := fs.String("version", "", "human version, e.g. 1.2.0")
	build := fs.Int64("build", 0, "monotonic build number (must increase each release)")
	url := fs.String("url", "", "same-origin download path (default /agent/download?v=<version>)")
	mandatory := fs.Bool("mandatory", false, "mark this update mandatory")
	out := fs.String("out", "manifest.json", "path to write the signed manifest")
	_ = fs.Parse(args)

	if *in == "" || *version == "" || *build <= 0 {
		fmt.Fprintln(os.Stderr, "sign: -in, -version and -build (>0) are required")
		os.Exit(2)
	}
	priv, err := readKey(*keyPath)
	if err != nil {
		fatal(err)
	}
	bin, err := os.ReadFile(*in)
	if err != nil {
		fatal(err)
	}
	dlURL := *url
	if dlURL == "" {
		dlURL = "/agent/download?v=" + *version
	}
	sum := sha256.Sum256(bin)
	m := shared.Manifest{
		Version:   *version,
		Build:     *build,
		SHA256:    hex.EncodeToString(sum[:]),
		Timestamp: time.Now().Unix(),
		Mandatory: *mandatory,
		URL:       dlURL,
	}
	if !update.IsSameOrigin(m.URL) {
		fmt.Fprintf(os.Stderr, "sign: url %q must be server-relative (start with a single /)\n", m.URL)
		os.Exit(2)
	}
	sm := update.Sign(m, priv)
	data, _ := json.MarshalIndent(sm, "", "  ")
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("Signed %s (build %d, sha256 %s…) → %s\n", *version, *build, m.SHA256[:12], *out)
}

func readKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(string(b))
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("key is %d bytes, want %d", len(raw), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "fst-sign:", err)
	os.Exit(1)
}
