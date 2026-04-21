$ErrorActionPreference = "Stop"

$composeFile = Join-Path $PSScriptRoot "install.yml"
if (-not (Test-Path $composeFile)) {
    Write-Host "install.yml not found next to the Towel installer files." -ForegroundColor Red
    exit 1
}

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Host "Docker is not installed or not on PATH." -ForegroundColor Yellow
    exit 1
}

Write-Host "Stopping Towel..."
& docker compose -f $composeFile down
exit $LASTEXITCODE
