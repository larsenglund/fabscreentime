//go:build !windows

package agent

// SingleInstance is a no-op off Windows (the named-mutex guard is Windows-only).
func SingleInstance(string) (release func(), ok bool) {
	return func() {}, true
}
