package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/larsenglund/fabscreentime/internal/shared"
)

// HTTPUploader posts batches to the backend's /api/ingest endpoint, authenticated
// with the device's API bearer token (PLAN.md §6.3).
type HTTPUploader struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

// NewHTTPUploader returns an uploader targeting baseURL (e.g. https://host),
// authenticating with the per-device API token.
func NewHTTPUploader(baseURL, token string) *HTTPUploader {
	return &HTTPUploader{
		BaseURL: baseURL,
		Token:   token,
		Client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Upload implements Uploader.
func (h *HTTPUploader) Upload(ctx context.Context, req shared.IngestRequest) (shared.IngestResponse, error) {
	var out shared.IngestResponse
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.BaseURL+"/api/ingest", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if h.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+h.Token)
	}
	resp, err := h.Client.Do(httpReq)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return out, fmt.Errorf("ingest status %d: %s", resp.StatusCode, msg)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}
