#!/bin/sh
# Start OMSI + Tracker. Put this in your openOMSI game folder with
# live_tracker_bridge-linux beside it, then run it (or double-click it).
# First run asks for your exact in-game driver name once and saves it.
set -eu
cd "$(dirname -- "$0")"

PANEL="https://omsi-admin.nextstoplabs.org"

if [ ! -f driver.txt ]; then
  printf 'First run: type your exact in-game driver name once. It is saved.\nDriver name: '
  read -r DRIVER
  printf '%s' "$DRIVER" > driver.txt
fi
DRIVER="$(cat driver.txt)"
echo "Driver: \"$DRIVER\""

if [ ! -x ./live_tracker_bridge-linux ]; then
  if [ -f ./live_tracker_bridge-linux ]; then
    chmod +x ./live_tracker_bridge-linux
  else
    echo "Missing live_tracker_bridge-linux. Put it next to this file."
    echo "Get it at $PANEL/tracker/live_tracker_bridge-linux"
    exit 1
  fi
fi
if [ ! -f ./plugins/live_tracker.lua ]; then
  echo "NOTE: ./plugins/live_tracker.lua not found. The tracker will run but"
  echo "the game sends it nothing until you install the plugin. Get it at"
  echo "$PANEL/tracker/live_tracker.lua and put it in a plugins folder here."
fi

# Bridge in the background. Stop it with: pkill -f live_tracker_bridge-linux
./live_tracker_bridge-linux -panel "$PANEL" -driver "$DRIVER" &
echo "Tracker started (background). Keep this window open while you drive."

# Now the game itself, whichever binary this install has.
if [ -x ./openomsi-launcher ]; then
  exec ./openomsi-launcher
elif [ -x ./openomsi ]; then
  exec ./openomsi
else
  echo "Started the tracker. I could not find the game binary here, so start"
  echo "openOMSI yourself."
fi
