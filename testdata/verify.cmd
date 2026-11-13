@echo off
rem Smoke test: bare open + drop-sim, both auto-close, exit codes into last-run.log
rem MUST run in an INTERACTIVE desktop session. In Session 0 (SSH / service)
rem the window can not be created and preview.exe exits with code 8.
rem Run it from a scheduled task with /it, or by double clicking.
setlocal
cd /d "%~dp0"

set EXE=%~dp0preview.exe
set SIM=%~dp0testdata\sample.md
set LOG=%~dp0last-run.log

if not exist "%EXE%" (
  echo preview.exe not found: %EXE%
  echo build it first:  go build -o preview.exe .
  exit /b 2
)

echo ==== BEGIN %DATE% %TIME% ==== >> "%LOG%"
echo [1] bare open, auto close 5s >> "%LOG%"
"%EXE%" --auto-close 5 >> "%LOG%" 2>&1
echo EXITCODE1=%ERRORLEVEL% >> "%LOG%"

echo [2] drop-sim text file, auto close 5s >> "%LOG%"
"%EXE%" --drop-sim "%SIM%" --auto-close 5 >> "%LOG%" 2>&1
echo EXITCODE2=%ERRORLEVEL% >> "%LOG%"

rem never leave a stray window behind if --auto-close failed to fire
taskkill /F /IM preview.exe >nul 2>&1
echo ==== END ==== >> "%LOG%"
type "%LOG%"
