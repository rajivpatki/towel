@echo off
setlocal
powershell.exe -ExecutionPolicy Bypass -File "%~dp0install_windows.ps1"
exit /b %ERRORLEVEL%
