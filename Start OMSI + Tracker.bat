@echo off
REM Start OMSI + Tracker. Double-click this. Put it in your openOMSI game
REM folder, next to openomsi.exe, with live_tracker_bridge.exe beside it.
setlocal EnableDelayedExpansion
cd /d "%~dp0"

set PANEL=https://omsi-admin.nextstoplabs.org

REM Ask for the driver name once, remember it in driver.txt next to this file.
if not exist driver.txt (
  echo First run: type your exact in-game driver name once. It is saved.
  set /p DRIVER=Driver name:
  >driver.txt echo !DRIVER!
)
set /p DRIVER=<driver.txt
echo Driver: "%DRIVER%"

if not exist live_tracker_bridge.exe (
  echo.
  echo Missing live_tracker_bridge.exe. Put it next to this file.
  echo Get it at %PANEL%/tracker/live_tracker_bridge.exe
  echo.
  pause
  exit /b 1
)
if not exist plugins\live_tracker.lua (
  echo.
  echo NOTE: plugins\live_tracker.lua not found. The tracker will run but the
  echo game sends it nothing until you install the plugin. Get it at
  echo %PANEL%/tracker/live_tracker.lua and put it in a plugins folder here.
  echo.
)

REM Bridge in its own window. Keep that window open while you drive.
start "OMSI Live Tracker" live_tracker_bridge.exe -panel %PANEL% -driver "%DRIVER%"

REM Now the game itself, whichever exe this install has.
if exist openomsi-launcher.exe (
  start "" openomsi-launcher.exe
  goto done
)
if exist openomsi.exe (
  start "" openomsi.exe
  goto done
)
echo.
echo Started the tracker. I could not find the game exe here, so start
echo openOMSI yourself. The tracker window must stay open while you drive.

:done
endlocal
