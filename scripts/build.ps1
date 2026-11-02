# Builds the React app, embeds it, and produces bin\jobhunt.exe.
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location (Join-Path $root "web")
try {
    if (-not (Test-Path node_modules)) { npm ci; if ($LASTEXITCODE) { exit 1 } }
    npm run build; if ($LASTEXITCODE) { exit 1 }
} finally { Pop-Location }
Push-Location $root
try {
    go build -o bin\jobhunt.exe .\cmd\jobhunt; if ($LASTEXITCODE) { exit 1 }
} finally { Pop-Location }
Write-Host "Built bin\jobhunt.exe"
