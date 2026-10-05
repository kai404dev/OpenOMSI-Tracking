@echo off
REM Start OMSI + Tracker. Double-click this. Put it in your openOMSI game
REM folder, next to openomsi.exe, with live_tracker_bridge.exe beside it.
REM The bridge finds your driver name by itself. To pin one name instead,
REM make a shortcut that adds: -driver "Your Name"
setlocal
cd /d "%~dp0"

set PANEL=https://omsi-admin.nextstoplabs.org

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
start "OMSI Live Tracker" live_tracker_bridge.exe -panel %PANEL%

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
