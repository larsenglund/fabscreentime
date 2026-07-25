package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyUpdateAtSwapsAndKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agent.bin")
	if err := os.WriteFile(exe, []byte("OLD VERSION"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := applyUpdateAt(exe, []byte("NEW VERSION")); err != nil {
		t.Fatalf("applyUpdateAt: %v", err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "NEW VERSION" {
		t.Fatalf("exe content = %q, want NEW VERSION", got)
	}
	old, err := os.ReadFile(exe + ".old")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(old) != "OLD VERSION" {
		t.Fatalf("backup content = %q, want OLD VERSION", old)
	}

	// A second update should overwrite the stale .old cleanly.
	if err := applyUpdateAt(exe, []byte("NEWER")); err != nil {
		t.Fatalf("second applyUpdateAt: %v", err)
	}
	got, _ = os.ReadFile(exe)
	if string(got) != "NEWER" {
		t.Fatalf("exe content = %q, want NEWER", got)
	}
}

func TestDownload(t *testing.T) {
	payload := []byte("agent-binary-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agent/download" {
			_, _ = w.Write(payload)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	u := NewSelfUpdater(srv.URL, 1, 0, nil)
	got, err := u.download(context.Background(), "/agent/download")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("download got %q, want %q", got, payload)
	}

	if _, err := u.download(context.Background(), "/nope"); err == nil {
		t.Fatal("want error on 404, got nil")
	}
}
