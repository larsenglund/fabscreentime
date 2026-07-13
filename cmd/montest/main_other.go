//go:build !windows

package main

import "fmt"

// montest exercises Windows-only display APIs; this stub keeps `go build ./...`
// and `go vet ./...` green on Linux/macOS/CI.
func main() {
	fmt.Println("montest is a Windows-only probe (PLAN.md §0.1 / Phase 0). Build it with GOOS=windows GOARCH=amd64.")
}
