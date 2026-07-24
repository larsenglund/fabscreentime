package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

// enrollFile is the sidecar the website installer drops next to the agent: it
// carries the one-time enrollment secret (in the file BODY, never a URL — §7.3)
// and is consumed and deleted on first run.
const enrollFile = "enroll.json"

// ErrEnrollRejected is returned when the backend refuses the secret (expired,
// already used, or unknown).
var ErrEnrollRejected = errors.New("enrollment rejected by server")

// EnrollConfig is the on-disk enroll.json format.
type EnrollConfig struct {
	Server       string `json:"server"`
	EnrollSecret string `json:"enroll_secret"`
}

// ReadEnrollFile loads enroll.json from dir, or (nil, nil) if absent.
func ReadEnrollFile(dir string) (*EnrollConfig, error) {
	data, err := os.ReadFile(filepath.Join(dir, enrollFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c EnrollConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// RemoveEnrollFile deletes the consumed sidecar so a stale secret can't be
// replayed. Best-effort: a leftover secret is already void server-side.
func RemoveEnrollFile(dir string) {
	_ = os.Remove(filepath.Join(dir, enrollFile))
}

// Enroll exchanges a one-time secret for durable credentials via POST
// /api/enroll. It is retried by the caller; a 401 (ErrEnrollRejected) is
// terminal and must not be retried.
func Enroll(ctx context.Context, baseURL, secret, hostname string) (*Credentials, error) {
	body, _ := json.Marshal(shared.EnrollRequest{EnrollSecret: secret, Hostname: hostname})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/enroll", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrEnrollRejected
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("enroll status %d: %s", resp.StatusCode, msg)
	}
	var out shared.EnrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.DeviceUUID == "" || out.APIToken == "" {
		return nil, fmt.Errorf("enroll: server returned empty credentials")
	}
	return &Credentials{DeviceUUID: out.DeviceUUID, APIToken: out.APIToken}, nil
}
