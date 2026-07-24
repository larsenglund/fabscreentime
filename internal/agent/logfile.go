package agent

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

// logFileName is the rolling agent log under the data directory. The silent
// (GUI-subsystem) build has no console, so without this every message —
// including a fatal startup error — goes nowhere and the agent is undiagnosable
// once installed (PLAN.md §4.6).
const logFileName = "agent.log"

// maxLogBytes caps the log; on startup an oversized file is truncated so the log
// can never grow unbounded on a long-lived install.
const maxLogBytes = 1 << 20 // 1 MiB

// SetupFileLogging routes the standard logger to dir/agent.log (in addition to
// stderr, which is only visible in console/dev runs). It returns the open file
// so main can defer Close. On any failure it leaves logging as-is and returns
// nil — logging must never be the reason the agent won't start.
func SetupFileLogging(dir string) *os.File {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	path := filepath.Join(dir, logFileName)
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogBytes {
		_ = os.Truncate(path, 0)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	// The file is the primary sink and must never be starved. io.MultiWriter
	// aborts on the first writer's error, and the GUI-subsystem build has no
	// valid stderr — so a plain MultiWriter(os.Stderr, f) writes nothing to the
	// file. Wrap stderr so its (expected) failures are swallowed, keeping the
	// file write reachable; stderr still works in console/dev runs.
	log.SetOutput(io.MultiWriter(tolerantWriter{os.Stderr}, f))
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	return f
}

// tolerantWriter forwards to w but never reports an error, so a dead stderr
// under the GUI subsystem can't break a downstream writer in an io.MultiWriter.
type tolerantWriter struct{ w io.Writer }

func (t tolerantWriter) Write(p []byte) (int, error) {
	_, _ = t.w.Write(p)
	return len(p), nil
}
