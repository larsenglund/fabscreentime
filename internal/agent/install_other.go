//go:build !windows

package agent

import (
	"errors"
	"os"
	"path/filepath"
)

var errWindowsOnly = errors.New("install/uninstall is Windows-only")

// InstallDir returns a per-user data location off Windows (for dev runs).
func InstallDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "FabScreenTime")
	}
	return ".fabscreentime"
}

// Install is a no-op off Windows.
func Install(string) error { return errWindowsOnly }

// Uninstall is a no-op off Windows.
func Uninstall() error { return errWindowsOnly }
