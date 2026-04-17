$ErrorActionPreference = "SilentlyContinue"
$needsReboot = $false
$composeFile = Join-Path $PSScriptRoot "install.yml"
if (-not (Test-Path $composeFile)) {
    $composeFile = Join-Path $PSScriptRoot "docker-compose.yml"
}

# Self-elevate to Administrator if not already
if (-not ([Security.Principal.WindowsPrincipal] `
    [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole( `
    [Security.Principal.WindowsBuiltInRole]::Administrator)) {

    Start-Process powershell `
        -ArgumentList "-ExecutionPolicy Bypass -File `"$PSCommandPath`"" `
        -Verb RunAs
    exit
}

# Ensure Chocolatey exists
if (-not (Get-Command choco.exe -ErrorAction SilentlyContinue)) {
    Set-ExecutionPolicy Bypass -Scope Process -Force
    [System.Net.ServicePointManager]::SecurityProtocol = [System.Net.SecurityProtocolType]::Tls12
    Invoke-Expression ((New-Object System.Net.WebClient).DownloadString('https://community.chocolatey.org/install.ps1'))
    $env:Path += ";$env:ALLUSERSPROFILE\chocolatey\bin"
}

# Ensure WSL optional features are enabled only if needed
$wslFeature = Get-WindowsOptionalFeature -Online -FeatureName Microsoft-Windows-Subsystem-Linux
if ($wslFeature.State -ne "Enabled") {
    Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Windows-Subsystem-Linux -All -NoRestart | Out-Null
    $needsReboot = $true
}

$vmpFeature = Get-WindowsOptionalFeature -Online -FeatureName VirtualMachinePlatform
if ($vmpFeature.State -ne "Enabled") {
    Enable-WindowsOptionalFeature -Online -FeatureName VirtualMachinePlatform -All -NoRestart | Out-Null
    $needsReboot = $true
}

# Install/update WSL without installing Ubuntu or any other distro
wsl.exe --install --no-distribution | Out-Null
wsl.exe --update | Out-Null
wsl.exe --set-default-version 2 | Out-Null

# Install Docker Desktop only if missing
if (-not (Test-Path "$Env:ProgramFiles\Docker\Docker\Docker Desktop.exe")) {
    choco install docker-desktop -y --no-progress
}

if ($needsReboot) {
    Write-Host ""
    Write-Host "============================================================" -ForegroundColor Yellow
    Write-Host "WARNING: You MAY need to restart your PC for Docker to work." -ForegroundColor Yellow
    Write-Host "============================================================" -ForegroundColor Yellow
    Write-Host ""
}

$dockerDesktop = "$Env:ProgramFiles\Docker\Docker\Docker Desktop.exe"
if (Test-Path $dockerDesktop) {
    Start-Process $dockerDesktop | Out-Null
}

Write-Host "Waiting for Docker to become available..."
$dockerReady = $false
for ($i = 0; $i -lt 60; $i++) {
    & docker info *> $null
    if ($LASTEXITCODE -eq 0) {
        $dockerReady = $true
        break
    }

    Start-Sleep -Seconds 5
}

if (-not $dockerReady) {
    Write-Host "Docker is not ready yet. Start Docker Desktop, then run the Compose command again." -ForegroundColor Yellow
    exit 1
}

Write-Host "Starting Towel..."
& docker compose -f $composeFile up -d
if ($LASTEXITCODE -ne 0) {
    Write-Host "Docker Compose failed to start the app." -ForegroundColor Red
    exit 1
}

Write-Host "Waiting for http://localhost:3000 ..."
for ($i = 0; $i -lt 60; $i++) {
    try {
        $response = Invoke-WebRequest -Uri "http://localhost:3000" -UseBasicParsing -TimeoutSec 2
        if ($response.StatusCode -ge 200) {
            break
        }
    } catch {
    }

    Start-Sleep -Seconds 2
}

Start-Process "http://localhost:3000"