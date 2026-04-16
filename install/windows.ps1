$ErrorActionPreference = "SilentlyContinue"
$needsReboot = $false

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

# Ask before rebooting, only if this script changed WSL-related Windows features
if ($needsReboot) {
    Write-Host ""
    Write-Host "A restart is required to complete WSL/Docker setup." -ForegroundColor Yellow
    $choice = Read-Host "Do you want to reboot now? (Y/N)"

    if ($choice -match '^[Yy]$') {
        shutdown.exe /r /t 5 /c "Restarting to complete WSL/Docker setup"
    } else {
        Write-Host "Please restart later to complete installation." -ForegroundColor Cyan
    }
} else {
    Write-Host "Setup complete. No reboot required." -ForegroundColor Green
}