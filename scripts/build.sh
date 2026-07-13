#!/usr/bin/env bash
# Build all FabScreenTime artifacts into ./dist.
#   - fabscreentimed : Linux backend binary
#   - agent.exe      : Windows agent, GUI subsystem (no console/tray window)
#   - montest.exe    : Windows Phase 0 monitor-signal probe
#
# Usage: scripts/build.sh [version]
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-0.0.0-dev}"
OUT=dist
mkdir -p "$OUT"

echo "Building backend (linux/amd64)…"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags "-s -w" -o "$OUT/fabscreentimed" ./cmd/fabscreentimed

echo "Building agent ${VERSION} (windows/amd64, GUI subsystem — silent)…"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -ldflags "-s -w -H=windowsgui -X main.Version=${VERSION}" -o "$OUT/agent.exe" ./cmd/agent

echo "Building montest probe (windows/amd64)…"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -ldflags "-s -w" -o "$OUT/montest.exe" ./cmd/montest

echo "Done:"
ls -la "$OUT"
