package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialsRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// No file yet → not enrolled.
	if c, err := LoadCredentials(dir); err != nil || c != nil {
		t.Fatalf("LoadCredentials on empty dir = %v, %v; want nil, nil", c, err)
	}

	want := &Credentials{DeviceUUID: "dev-uuid-123", APIToken: "super-secret-token"}
	if err := SaveCredentials(dir, want); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	got, err := LoadCredentials(dir)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if got == nil || got.DeviceUUID != want.DeviceUUID || got.APIToken != want.APIToken {
		t.Fatalf("round-trip = %+v, want uuid=%s token=%s", got, want.DeviceUUID, want.APIToken)
	}
}

func TestCredentialsTokenNotStoredInClear(t *testing.T) {
	dir := t.TempDir()
	if err := SaveCredentials(dir, &Credentials{DeviceUUID: "d", APIToken: "PLAINTEXTTOKEN"}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, credsFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("PLAINTEXTTOKEN")) {
		t.Fatalf("API token written in clear:\n%s", data)
	}
}
