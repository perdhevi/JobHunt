@echo off
rem Runs docker-up.ps1 on machines where PowerShell scripts are disabled.
rem The bypass applies to this one run; the system policy isn't changed.
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0docker-up.ps1" %*
