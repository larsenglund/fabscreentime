// Package web embeds the built dashboard SPA (web/dist) into the backend binary,
// so the single fabscreentimed binary serves the API and the UI off one port
// with no Node in production (PLAN.md §7). The dist/ directory is a committed
// build artifact — rebuild it with scripts/build-ui.ps1 after changing web/src.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// DistFS returns the built SPA filesystem rooted at dist/.
func DistFS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
