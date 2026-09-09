package agent

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupFileLoggingCapturesOutput(t *testing.T) {
	// Restore the default logger for other tests.
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	dir := t.TempDir()
	f := SetupFileLogging(dir)
	if f == nil {
		t.Fatal("SetupFileLogging returned nil")
	}
	defer f.Close()

	log.Print("hello-from-test")

	data, err := os.ReadFile(filepath.Join(dir, logFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hello-from-test") {
		t.Fatalf("log line not written to file; got %q", data)
	}
}

// The GUI-subsystem build has no valid stderr; a plain io.MultiWriter would abort
// on the stderr write and never reach the file. tolerantWriter must swallow the
// error so the file sink downstream still runs.
func TestTolerantWriterSwallowsErrors(t *testing.T) {
	tw := tolerantWriter{w: failWriter{}}
	n, err := tw.Write([]byte("abc"))
	if err != nil {
		t.Fatalf("tolerantWriter surfaced an error: %v", err)
	}
	if n != 3 {
		t.Fatalf("tolerantWriter reported n=%d, want 3", n)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("no stderr") }

// The run that fails is the one worth reading, and it is always followed by a
// restart. Truncating at startup destroyed exactly that evidence.
func TestSetupFileLoggingKeepsThePreviousRun(t *testing.T) {
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	dir := t.TempDir()
	path := filepath.Join(dir, logFileName)
	oversized := append(make([]byte, maxLogBytes), []byte("what-the-last-run-was-doing")...)
	if err := os.WriteFile(path, oversized, 0o600); err != nil {
		t.Fatal(err)
	}

	f := SetupFileLogging(dir)
	if f == nil {
		t.Fatal("SetupFileLogging returned nil")
	}
	defer f.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > maxLogBytes {
		t.Errorf("oversized log was not rotated: still %d bytes", fi.Size())
	}
	prev, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("previous run's log was not kept: %v", err)
	}
	if !strings.Contains(string(prev), "what-the-last-run-was-doing") {
		t.Error("agent.log.1 does not hold the previous run's output")
	}
}

// A long-lived agent never reaches the startup check, so the writer has to
// rotate on its own or the "bounded" log grows without bound.
func TestRotatingWriterRotatesWhileRunning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, logFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w := &rotatingWriter{f: f, path: path}
	chunk := make([]byte, 64<<10)
	for written := 0; written <= 2*maxLogBytes; written += len(chunk) {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	w.f.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > maxLogBytes {
		t.Errorf("log grew to %d bytes without rotating (cap %d)", fi.Size(), maxLogBytes)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("no rotated generation kept: %v", err)
	}
}
