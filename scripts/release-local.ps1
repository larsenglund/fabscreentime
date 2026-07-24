<#
.SYNOPSIS
  Build, sign, and stage a new silent agent so running agents auto-update.

.DESCRIPTION
  This is the local development release loop (PLAN.md §5). Each run:
    1. builds the silent (GUI-subsystem) agent.exe with a monotonically
       increasing build number (Unix epoch seconds — always greater than the
       last run's, which is exactly what the anti-rollback check wants),
    2. signs it offline with release.key into manifest.json,
    3. stages both into the backend's -agentdir.

  The backend hot-reloads manifest.json on mtime change (no restart), so within
  one ingest cycle every enrolled agent sees the newer build, verifies it against
  its pinned key, and self-updates. Run this whenever you change the client.

  release.key must already exist (create it once with:
    go run .\cmd\fst-sign genkey -out release.key
  and pin the printed public key in internal/agent/pinnedkeys.go). Keep the
  private key OUT of git.

.EXAMPLE
  .\scripts\release-local.ps1 -Version 0.3.0
#>
[CmdletBinding()]
param(
    [string]$Version = "0.1.0-dev",
    [string]$AgentDir = "agentrelease",
    [string]$Key = "release.key"
)

$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

# Go is not always on PATH in every shell on this machine.
$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) { $go = "C:\Program Files\Go\bin\go.exe" }
if (-not (Test-Path $go)) { throw "go not found (looked on PATH and at $go)" }

if (-not (Test-Path $Key)) {
    throw "$Key not found. Create it once with: & '$go' run .\cmd\fst-sign genkey -out $Key  (then pin the public key in internal/agent/pinnedkeys.go)"
}

New-Item -ItemType Directory -Force -Path $AgentDir | Out-Null
$exe = Join-Path $AgentDir "agent.exe"
$manifest = Join-Path $AgentDir "manifest.json"

# Monotonic build number: Unix epoch seconds is strictly increasing per run, so a
# freshly staged release always out-ranks whatever the running agent carries.
$build = [int64][DateTimeOffset]::UtcNow.ToUnixTimeSeconds()

Write-Host "Building silent agent $Version (build $build)..."
$env:CGO_ENABLED = "0"; $env:GOOS = "windows"; $env:GOARCH = "amd64"
& $go build -ldflags "-s -w -H=windowsgui -X main.Version=$Version -X main.Build=$build" -o $exe .\cmd\agent
if ($LASTEXITCODE -ne 0) { throw "agent build failed" }

Write-Host "Signing -> $manifest..."
& $go run .\cmd\fst-sign sign -key $Key -in $exe -version $Version -build $build -out $manifest
if ($LASTEXITCODE -ne 0) { throw "signing failed" }

Write-Host ""
Write-Host "Staged $Version (build $build) into $AgentDir. Point the backend at it with:"
Write-Host "  go run .\cmd\fabscreentimed -db .\dev.db -agentdir $AgentDir"
Write-Host "Running agents will verify and self-update within one ingest cycle."
