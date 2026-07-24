//go:build windows

package agent

import (
	"encoding/base64"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DPAPI token-at-rest protection (PLAN.md §4.8). CryptProtectData encrypts under
// the current *user's* key, so the ciphertext is unreadable by another user on a
// shared family PC — exactly the threat the plan calls out. The blob is stored
// base64 in credentials.json.

var (
	crypt32 = windows.NewLazySystemDLL("crypt32.dll")

	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
)

const cryptprotectUIForbidden = 0x1 // never prompt; fail instead (headless agent)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func (b dataBlob) bytes() []byte {
	if b.cbData == 0 || b.pbData == nil {
		return nil
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

func newBlob(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

// protect DPAPI-encrypts plain and returns base64(ciphertext). An empty string
// round-trips to empty (no token yet).
func protect(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	in := newBlob([]byte(plain))
	var out dataBlob
	r, _, err := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0,
		cryptprotectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return "", fmt.Errorf("CryptProtectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.pbData)))
	return base64.StdEncoding.EncodeToString(out.bytes()), nil
}

// unprotect reverses protect.
func unprotect(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	in := newBlob(raw)
	var out dataBlob
	r, _, err := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0,
		cryptprotectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return "", fmt.Errorf("CryptUnprotectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.pbData)))
	return string(out.bytes()), nil
}
