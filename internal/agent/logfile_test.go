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
