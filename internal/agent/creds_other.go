//go:build !windows

package agent

import "encoding/base64"

// Off Windows there is no DPAPI; the token is stored base64 (obfuscation only,
// not encryption) behind the file's 0600 permissions. This path exists for dev
// and CI — the real deployment target is Windows, where protect() uses DPAPI.
func protect(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	return base64.StdEncoding.EncodeToString([]byte(plain)), nil
}

func unprotect(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
