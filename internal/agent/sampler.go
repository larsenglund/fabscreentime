package agent

// Reading is one raw observation from the platform sampler. All hard platform
// code lives behind this interface so the agent core is unit-testable on any OS
// with a fake sampler (PLAN.md §4.1).
type Reading struct {
	MonitorsActive int // active-display count (§4.4a); -1 = not sampled
	DisplayPower   int // GUID_SESSION_DISPLAY_STATUS: 0 off, 1 on, 2 dimmed (§4.4b); -1 = not sampled

	// DDCPower is the raw DDC/CI probe result — the signal that detects a
	// physical monitor power-off on hardware where the display never leaves
	// the topology (all of it, so far: MONTEST-RESULTS.md). Values:
	//   1     VCP 0xD6 says "on"
	//   2..5  VCP 0xD6 says standby/suspend/off — direct off testimony
	//   -1    no physical-monitor handle (DP off signature; also RDP/VMs)
	//   -2    handle present but the DDC query failed (Philips chronic ON state)
	//   -3/0  not sampled (non-Windows, dxva2 missing, or first seconds)
	DDCPower int
	// DDCArmed reports whether a VCP power reply has been seen this session.
	// It licenses trusting DDCPower==-1 as "off": on hardware where DDC never
	// works (VMs, RDP, quirky panels) the -1 is chronic and must NOT read as
	// off, or screentime would silently zero (see ddcSaysOff).
	DDCArmed bool

	IsIdle      bool  // derived from GetLastInputInfo vs threshold
	IdleMS      int64 // raw idle milliseconds, so the threshold can be re-tuned server-side
	ExeName     string
	WindowTitle string
}

// DDCPower sentinel values (also part of the wire contract, shared.Sample).
const (
	DDCNoHandle    = -1
	DDCQueryFailed = -2
	DDCNotSampled  = -3
)

// Sampler produces one Reading per call. Implemented by the real Win32 sampler
// on Windows and by a deterministic stub elsewhere.
type Sampler interface {
	Sample() (Reading, error)
}

// IdleThreshold is the cutoff for IsIdle. Matches the ~60s sample cadence.
const IdleThresholdMS = 60_000
