//go:build !windows

package agent

// The stub sampler lets the whole agent build, unit-test, and run end-to-end on
// Linux/CI (PLAN.md §4.1). It fabricates plausible readings so a developer can
// exercise the full ingest path without a Windows box. Monitor fields are -1
// ("not sampled") — real monitor detection is Windows-only and lands in Phase 2.

var stubApps = []struct{ exe, title string }{
	{"chrome.exe", "FabScreenTime — GitHub"},
	{"Code.exe", "PLAN.md — fabscreentime — Visual Studio Code"},
	{"game.exe", "Some Game"},
	{"explorer.exe", "Downloads"},
}

type stubSampler struct{ n int }

// NewSampler returns the platform sampler. On non-Windows builds this is the stub.
func NewSampler() Sampler { return &stubSampler{} }

func (s *stubSampler) Sample() (Reading, error) {
	app := stubApps[s.n%len(stubApps)]
	idle := s.n%4 == 0
	idleMS := int64(0)
	if idle {
		idleMS = int64(IdleThresholdMS + 30_000)
	}
	s.n++
	return Reading{
		MonitorsActive: -1,
		DisplayPower:   -1,
		IsIdle:         idle,
		IdleMS:         idleMS,
		ExeName:        app.exe,
		WindowTitle:    app.title,
	}, nil
}
