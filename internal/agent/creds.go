package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// credsFile is the on-disk name of the durable device credentials, stored in the
// agent's data directory.
const credsFile = "credentials.json"

// Credentials are the durable per-device identity issued at enrollment: the
// server-assigned UUID plus the API bearer token used for every ingest. The
// token is the sensitive secret; at rest it is protected by the platform key
// store (DPAPI on Windows — PLAN.md §4.8) via protect/unprotect, so another user
// on a shared machine cannot read or impersonate it.
type Credentials struct {
	DeviceUUID string `json:"device_uuid"`
	APIToken   string `json:"-"` // never serialized in the clear; see Protected

	// Protected is the platform-encrypted API token as written to disk.
	Protected string `json:"api_token_protected"`
}

// LoadCredentials reads the stored credentials, or (nil, nil) if none exist yet
// (the pre-enrollment state). A present-but-unreadable token is an error.
func LoadCredentials(dir string) (*Credentials, error) {
	data, err := os.ReadFile(filepath.Join(dir, credsFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.DeviceUUID == "" || c.Protected == "" {
		return nil, nil // treat a partial file as "not enrolled"
	}
	token, err := unprotect(c.Protected)
	if err != nil {
		return nil, err
	}
	c.APIToken = token
	return &c, nil
}

// SaveCredentials writes credentials with the token protected at rest (0600).
func SaveCredentials(dir string, c *Credentials) error {
	protectedToken, err := protect(c.APIToken)
	if err != nil {
		return err
	}
	out := Credentials{DeviceUUID: c.DeviceUUID, Protected: protectedToken}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, credsFile), data, 0o600)
}
