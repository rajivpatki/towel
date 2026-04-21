[CmdletBinding()]
param(
    [string]$Version = "0.1.0",
    [string]$OutputDir = (Join-Path $PSScriptRoot "..\dist")
)

$ErrorActionPreference = "Stop"

function Resolve-InnoSetupCompiler {
    $command = Get-Command iscc.exe -ErrorAction SilentlyContinue
    if ($command) {
        return $command.Source
    }

    $candidates = @(
        "C:\Program Files (x86)\Inno Setup 6\ISCC.exe",
        "C:\Program Files\Inno Setup 6\ISCC.exe"
    )

    foreach ($candidate in $candidates) {
        if (Test-Path $candidate) {
            return $candidate
        }
    }

    throw "Inno Setup compiler not found. Install Inno Setup 6 and rerun this script."
}

$compiler = Resolve-InnoSetupCompiler
$scriptPath = Join-Path $PSScriptRoot "Towel.iss"
$resolvedOutputDir = [System.IO.Path]::GetFullPath($OutputDir)

New-Item -ItemType Directory -Force -Path $resolvedOutputDir | Out-Null

$previousVersion = $env:TOWEL_VERSION
$env:TOWEL_VERSION = $Version

try {
    & $compiler "/O$resolvedOutputDir" $scriptPath
} finally {
    if ($null -eq $previousVersion) {
        Remove-Item Env:\TOWEL_VERSION -ErrorAction SilentlyContinue
    } else {
        $env:TOWEL_VERSION = $previousVersion
    }
}
