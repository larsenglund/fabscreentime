<#
.SYNOPSIS
  Build the dashboard SPA into web/dist (committed and embedded by the backend).

.DESCRIPTION
  The React dashboard (web/) builds to web/dist, which web/embed.go embeds into
  the fabscreentimed binary. web/dist is a COMMITTED build artifact so `go build`
  and CI never need Node. Run this after changing anything under web/src, then
  rebuild the backend and commit the updated web/dist.

  Node 20+ is required. This machine's modern Node lives at
  %LOCALAPPDATA%\Programs\nodejs22 (the system PATH may still point at an older
  Node), so the script prefers that install.

.EXAMPLE
  .\scripts\build-ui.ps1
#>
[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

$nodeDir = "$env:LOCALAPPDATA\Programs\nodejs22"
if (Test-Path "$nodeDir\node.exe") {
    $env:PATH = "$nodeDir;$env:PATH"
    $npm = "$nodeDir\npm.cmd"
} else {
    $npm = "npm"
}

$ver = (& node --version) 2>$null
if (-not $ver -or [int](($ver -replace '^v(\d+)\..*$', '$1')) -lt 20) {
    throw "Node 20+ required (found '$ver'). Install it or point this script at a modern Node."
}
Write-Host "Using Node $ver"

Push-Location web
try {
    if (-not (Test-Path node_modules)) { & $npm install }
    & $npm run build
    if ($LASTEXITCODE -ne 0) { throw "vite build failed" }
} finally {
    Pop-Location
}

Write-Host ""
Write-Host "Built web/dist. Rebuild the backend to embed it:"
Write-Host "  go build ./cmd/fabscreentimed"
Write-Host "Then commit the updated web/dist."
