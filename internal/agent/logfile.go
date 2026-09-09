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

// maxLogBytes caps one log generation. Two are kept (agent.log + agent.log.1),
// so the log is bounded at 2 MiB while still holding the run BEFORE the current
// one. That predecessor is the whole point: the interesting failures here end in
// a restart, and truncating at startup threw away the only record of what the
// agent was doing when it stopped.
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
	rotateIfLarge(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	// The file is the primary sink and must never be starved. io.MultiWriter
	// aborts on the first writer's error, and the GUI-subsystem build has no
	// valid stderr — so a plain MultiWriter(os.Stderr, f) writes nothing to the
	// file. Wrap stderr so its (expected) failures are swallowed, keeping the
	// file write reachable; stderr still works in console/dev runs.
	log.SetOutput(io.MultiWriter(tolerantWriter{os.Stderr}, &rotatingWriter{f: f, path: path}))
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	return f
}

// rotateIfLarge moves an oversized log aside as agent.log.1, replacing any
// previous generation. Rotating rather than truncating is what keeps the run
// that failed readable after the restart that fixed it.
func rotateIfLarge(path string) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() <= maxLogBytes {
		return
	}
	_ = os.Remove(path + ".1")
	if err := os.Rename(path, path+".1"); err != nil {
		_ = os.Truncate(path, 0) // rotation blocked (AV, open handle) — cap it anyway
	}
}

// rotatingWriter rotates the log while the agent runs, not only at startup. An
// agent that stays up for weeks never reaches the startup check, so without this
// the "bounded" log grows without bound exactly on the long-lived installs.
type rotatingWriter struct {
	f    *os.File
	path string
	n    int64 // bytes written since the last size check
}

// checkEvery bounds how often the writer stats the file — a syscall per log line
// would be silly for a log this quiet.
const checkEvery = 64 << 10 // 64 KiB

func (w *rotatingWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	w.n += int64(n)
	if w.n < checkEvery {
		return n, err
	}
	w.n = 0
	if fi, e := w.f.Stat(); e != nil || fi.Size() <= maxLogBytes {
		return n, err
	}
	// Reopen after rotating: the old handle keeps writing to the renamed file.
	w.f.Close()
	rotateIfLarge(w.path)
	if f, e := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); e == nil {
		w.f = f
	} else {
		// Reopen failed — put the old handle back so logging keeps going
		// somewhere rather than dying silently.
		if f, e := os.OpenFile(w.path+".1", os.O_WRONLY|os.O_APPEND, 0o600); e == nil {
			w.f = f
		}
	}
	return n, err
}

// tolerantWriter forwards to w but never reports an error, so a dead stderr
// under the GUI subsystem can't break a downstream writer in an io.MultiWriter.
type tolerantWriter struct{ w io.Writer }

func (t tolerantWriter) Write(p []byte) (int, error) {
	_, _ = t.w.Write(p)
	return len(p), nil
}
