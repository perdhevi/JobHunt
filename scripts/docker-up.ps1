# Exposes a Windows drive to Docker Desktop and starts jobhunt in a container.
#   .\scripts\docker-up.ps1               use the drive from .env, or G
#   .\scripts\docker-up.ps1 -Drive H      switch drive letter
#   .\scripts\docker-up.ps1 -Build        rebuild the image first
param(
    [ValidatePattern('^[A-Za-z]:?$')][string]$Drive,
    [switch]$Build
)
# Exit codes are checked by hand: docker writes progress to stderr, which
# $ErrorActionPreference = "Stop" would treat as a failure when output is captured.
$root = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $root ".env"

$lines = @()
if (Test-Path $envFile) { $lines = @(Get-Content $envFile) }
if (-not $Drive) {
    $saved = $lines | Where-Object { $_ -match '^JOBHUNT_DRIVE=' } | Select-Object -First 1
    $Drive = if ($saved) { $saved -replace '^JOBHUNT_DRIVE=', '' } else { "G" }
}
$letter = $Drive.Substring(0, 1).ToUpper()

if (-not (Test-Path "${letter}:\")) {
    throw "Drive ${letter}: isn't available. Is Google Drive running?"
}
docker info --format '{{.ServerVersion}}' | Out-Null
if ($LASTEXITCODE) { throw "Docker isn't running. Start Docker Desktop and try again." }

# Written without a BOM, which would corrupt the first variable name.
$lines = @($lines | Where-Object { $_ -notmatch '^JOBHUNT_DRIVE=' }) + "JOBHUNT_DRIVE=$letter"
[IO.File]::WriteAllText($envFile, (($lines -join "`n") + "`n"))

# The mount lasts until Docker Desktop restarts, so this runs on every start.
$lower = $letter.ToLower()
$mount = "grep -q ' /mnt/host/$lower 9p ' /proc/mounts || { mkdir -p /mnt/host/$lower && mount -t drvfs ${letter}: /mnt/host/$lower; }"
wsl -d docker-desktop -u root sh -c $mount
if ($LASTEXITCODE) { throw "Couldn't expose ${letter}: to Docker." }

Push-Location $root
try {
    # Recreate so the container binds the fresh mount, not one from before a restart.
    $up = @("compose", "up", "-d", "--force-recreate", "--wait")
    if ($Build) { $up += "--build" }
    docker @up
    if ($LASTEXITCODE) {
        docker compose logs --tail 20 jobhunt
        throw "jobhunt didn't start."
    }
} finally { Pop-Location }
Write-Host "Board at http://127.0.0.1:8787"
