//go:build windows

package agent

import "golang.org/x/sys/windows"

// SingleInstance acquires a named mutex so a Task Scheduler trigger and a
// self-update relaunch can't leave two agents running at once (PLAN.md §5.2).
// It returns a release func and ok=false if another instance already holds it.
func SingleInstance(name string) (release func(), ok bool) {
	noop := func() {}
	ptr, err := windows.UTF16PtrFromString("Global\\" + name)
	if err != nil {
		return noop, true // don't block startup on a naming failure
	}
	h, err := windows.CreateMutex(nil, false, ptr)
	if err == windows.ERROR_ALREADY_EXISTS {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return noop, false
	}
	if err != nil {
		return noop, true
	}
	return func() { windows.CloseHandle(h) }, true
}
