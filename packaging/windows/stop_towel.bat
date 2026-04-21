@echo off
setlocal
powershell.exe -ExecutionPolicy Bypass -File "%~dp0stop_towel.ps1"
exit /b %ERRORLEVEL%
