package agent

// Reading is one raw observation from the platform sampler. All hard platform
// code lives behind this interface so the agent core is unit-testable on any OS
// with a fake sampler (PLAN.md §4.1).
type Reading struct {
	MonitorsActive int   // active-display count (primary presence signal, §4.4a); -1 = not sampled
	DisplayPower   int   // GUID_SESSION_DISPLAY_STATUS: 0 off, 1 on, 2 dimmed (§4.4b); -1 = not sampled
	IsIdle         bool  // derived from GetLastInputInfo vs threshold
	IdleMS         int64 // raw idle milliseconds, so the threshold can be re-tuned server-side
	ExeName        string
	WindowTitle    string
}

// Sampler produces one Reading per call. Implemented by the real Win32 sampler
// on Windows and by a deterministic stub elsewhere.
type Sampler interface {
	Sample() (Reading, error)
}

// IdleThreshold is the cutoff for IsIdle. Matches the ~60s sample cadence.
const IdleThresholdMS = 60_000
