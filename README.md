# OMSI Live Tracker

Shows live drivers on your admin panel map: position, bus type, capacity,
fleet number, reg plate and passenger count.

## How it works

Your game server already publishes position, bus, line and speed for every
driver. No installs needed for that part.

Three things never leave the driver's own game: fleet number, reg plate and
passenger count. This reporter carries those three to the panel. It has two
parts and both run on the driver's PC:

1. `live_tracker.lua` (the plugin). Collects the numbers inside the game.
   The game only lets it whisper to its own PC, never the internet.
2. The bridge (the forwarder). Hears that whisper and posts it to the panel
   over normal HTTPS.

```
game plugin  --UDP to own PC-->  bridge  --HTTPS-->  admin panel
```

## Driver setup (pick Windows or Linux)

You need openOMSI **0.1.1668 or newer**. Older builds lack the send function
and the plugin will tell you so on screen instead of working.

**Windows (no Python needed):**

1. Make a `plugins` folder next to your `openomsi.exe` if there is none.
2. Download `live_tracker.lua` from
   `http://YOUR-PANEL:8118/tracker/live_tracker.lua` into that folder.
   The file name must end in exactly `.lua` (Windows sometimes sneaks on
   an extra `.txt`, which breaks it).
3. Download `live_tracker_bridge.exe` from
   `http://YOUR-PANEL:8118/tracker/live_tracker_bridge.exe` into the game
   folder, next to `Start OMSI + Tracker.bat`.
4. Double-click `Start OMSI + Tracker.bat`. Type your exact in-game driver
   name once when asked. It starts the tracker and opens the game.
   Keep the tracker window open while you drive.

**Linux:**

1. Same plugin step as above (`plugins/live_tracker.lua`).
2. Download `live_tracker_bridge-linux` from
   `http://YOUR-PANEL:8118/tracker/live_tracker_bridge-linux`, run
   `chmod +x` on it once.
3. Run `./start-omsi-tracker.sh` from the game folder. It asks for your
   driver name once, starts the tracker, opens the game.

Drive for 30 seconds. Your panel row gains fleet number, reg plate and
passengers. If the driver name you typed does not match your in-game name,
your data lands on a separate row, so spell it exactly right.

## If it does not work

Work down this list. Each step says what to check.

1. Plugin file wrong? The game log (`game.log`, search `live_tracker`)
   must show a `started` line. No line at all means wrong folder or a
   `.lua.txt` file name. An error naming `omsi.send` means the game is
   older than 0.1.1668, update it.
2. Bridge deaf? Run the bridge with `--debug` (add it after the exe name
   in a terminal). Drive. You must see `datagram:` lines. None means the
   plugin is not sending (back to step 1).
3. Panel refusing? The bridge prints `reply: HTTP ...` for every post.
   Anything but 200 is explained right there (bad token, bad address).

## Server side

Nothing to install. The panel takes tracker posts at `POST /api/live` and
merges them into `GET /api/live` by driver name. Set `live_token` in
`server.cfg` if you want a shared secret (drivers then add
`-token <it>`), or leave it empty on a trusted network.

Running your own server with this? Search the `.bat` and `.sh` for the
panel address and swap in yours, then host the files anywhere your drivers
can download them.

## Files in this repo

- `plugins/live_tracker.lua` - the in-game reporter
- `bridge-go/main.go` - the forwarder source (builds the .exe and -linux)
- `bridge/live_tracker_bridge.py` - same forwarder in Python, for
  macOS and anyone who prefers it
- `Start OMSI + Tracker.bat` - Windows one-click starter (tracker + game)
- `start-omsi-tracker.sh` - Linux one-click starter (tracker + game)
