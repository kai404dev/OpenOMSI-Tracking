#!/usr/bin/env python3
"""live_tracker_bridge.py - player-side forwarder for the openOMSI live map.

The Lua plugin (plugins/live_tracker.lua) can only send UDP datagrams to
127.0.0.1, never over the network. This small stdlib-only program runs on the
same PC as the game, listens on 127.0.0.1:47800, enriches each report and
POSTs it to the admin panel:

    game (Lua) --UDP 127.0.0.1:47800--> bridge --HTTPS--> panel POST /api/live

Enrichment done here (outside the Lua sandbox, which has no file access):
  * driver name (the Lua API does not expose the multiplayer player name,
    so you set it here - it must match your in-game name for map merging)
  * bus spec: resolves manufacturer+model to a Vehicles/**/*.bus file and
    reads [passengercabin] [passpos] entries for seats / standing / capacity,
    plus [mass] and the .bus path itself

Usage:
    python3 live_tracker_bridge.py --panel https://your-panel.example.com
        --driver "Your InGame Name" --omsi-root "C:/Program Files/OMSI 2"

    Optional: --token <live_token from server.cfg>  (required only if the
    panel has live_token set), --port 47800, --interval 2 (min seconds
    between POSTs; reports arriving faster are coalesced).

    Environment fallbacks: LIVE_PANEL, LIVE_DRIVER, LIVE_TOKEN, OMSI_ROOT.

Needs nothing but the Python standard library.
"""

import argparse
import json
import os
import re
import socket
import sys
import time
import urllib.request
from pathlib import Path

UDP_PORT = 47800
MAX_DATAGRAM = 65535
DBG = [False]

def dlog(msg):
    if DBG[0]:
        print(f"[bridge:dbg] {msg}", flush=True)


# ------------------------------------------------------------ bus resolution
def parse_friendlyname(bus_path: Path):
    """Return (manufacturer, model) from a .bus [friendlyname] section."""
    try:
        text = bus_path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return None, None
    lines = text.splitlines()
    for idx, line in enumerate(lines):
        if line.strip().lower() == "[friendlyname]":
            vals = []
            for follow in lines[idx + 1: idx + 6]:
                s = follow.strip()
                if not s or s.startswith("["):
                    break
                vals.append(s)
            if len(vals) >= 2:
                return vals[0], vals[1]
            return None, None
    return None, None


def scan_buses(omsi_root: Path):
    """Index Vehicles/**/*.bus by (manufacturer, model) -> bus file."""
    index = {}
    vdir = omsi_root / "Vehicles"
    if not vdir.is_dir():
        return index
    for bus in vdir.rglob("*.bus"):
        man, model = parse_friendlyname(bus)
        if man and model:
            index.setdefault((man.strip().lower(), model.strip().lower()), bus)
            # also index by model alone as a fallback key
            index.setdefault((None, model.strip().lower()), bus)
    return index


def read_sections(path: Path, section: str, width: int = 8):
    """Yield the raw value lines following each [section] header.

    A header is the keyword alone on its line: value lines may themselves
    start with brackets (e.g. a "[SP] Studio Polygon" manufacturer inside
    [friendlyname]), so a full-line match is required.
    """
    try:
        lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError:
        return
    want = f"[{section}]"
    for idx, line in enumerate(lines):
        s = line.strip()
        if not re.fullmatch(r"\[[A-Za-z_0-9-]+\]", s):
            continue
        if s.lower() != want:
            continue
        vals = []
        for follow in lines[idx + 1: idx + 1 + width]:
            fs = follow.strip()
            if re.fullmatch(r"\[[A-Za-z_0-9-]+\]", fs):
                break
            if not fs:
                continue  # blank lines are insignificant in OMSI cfgs
            vals.append(fs)
        yield vals


def count_passpos(cabin_cfg: Path):
    """Count (seats, standing) from [passpos] blocks. h==0 -> standing."""
    seats = standing = 0
    for vals in read_sections(cabin_cfg, "passpos", 8):
        if len(vals) < 4:
            continue
        try:
            h = float(vals[3].replace(",", "."))
        except ValueError:
            continue
        if h > 0.05:
            seats += 1
        else:
            standing += 1
    return seats, standing


def join_insensitive(base: Path, rel: str):
    """Join a Windows-style relative path case-insensitively (OMSI content
    mixes Model/ vs model/ and Linux filesystems care)."""
    parts = [p for p in rel.replace("\\", "/").split("/") if p not in ("", ".")]
    cur = base
    for part in parts:
        if part == "..":
            cur = cur.parent
            continue
        cand = cur / part
        if cand.exists():
            cur = cand
            continue
        if cur.is_dir():
            low = part.lower()
            match = next((c for c in cur.iterdir() if c.name.lower() == low), None)
            if match is not None:
                cur = match
                continue
        cur = cand  # keep unresolved path; caller checks is_file()
    return cur


def resolve_bus(index: dict, manufacturer, model):
    """Return spec dict for a bus, or {} if it cannot be resolved."""
    if not model:
        return {}
    key = ((manufacturer or "").strip().lower() or None,
           model.strip().lower())
    bus = index.get(key) or index.get((None, model.strip().lower()))
    if bus is None:
        # last resort: substring match on the model
        needle = model.strip().lower()
        for (man, mod), path in index.items():
            if mod and (needle in mod or mod in needle):
                bus = path
                break
    if bus is None:
        return {}
    spec = {"bus_file": str(bus).replace("\\", "/").split("Vehicles/", 1)[-1]}
    try:
        basedir = bus.parent
        cabins = [join_insensitive(basedir, v[0])
                  for v in read_sections(bus, "passengercabin", 4) if v]
        # articulated buses: follow [couple_back] into the trailer .bus
        for v in read_sections(bus, "couple_back", 4):
            if v and v[0].lower().endswith(".bus"):
                trail = join_insensitive(basedir, v[0])
                if trail.is_file():
                    cabins += [join_insensitive(trail.parent, w[0])
                               for w in read_sections(trail, "passengercabin", 4) if w]
        seats = standing = 0
        for cabin in cabins:
            if cabin.is_file():
                s, st = count_passpos(cabin)
                seats += s
                standing += st
        if seats or standing:
            spec["seats"] = seats
            spec["standing"] = standing
            spec["capacity"] = seats + standing
        for v in read_sections(bus, "mass", 4):
            try:
                spec["mass_t"] = float(v[0].replace(",", "."))  # "Masse in t"
            except (ValueError, IndexError):
                pass
            break
        man, mod = parse_friendlyname(bus)
        if man:
            spec["bus_manufacturer"] = man
        if mod:
            spec["bus_model"] = mod
    except OSError:
        pass
    return spec


# ------------------------------------------------- driver name auto-detect
# Same order the game itself uses (its lan::player_name): an explicit pin,
# else the launcher's active profile in ~/.openomsi, else the newest
# Drivers/*.odr personnel file beside the game (rewritten every session),
# else the computer account, else ask once. Re-checked on every post so
# switching driver profile mid-session just works. The Lua API cannot see
# the multiplayer name, hence this disk lookup.
_PROFILE_KEYS = {"profile", "driver_profile", "active_profile", "driver",
                 "player", "player_name", "lan_name", "pilot"}

def _usable_name(s):
    t = (s or "").strip()
    return t if (t and t.lower() != "driver") else ""

def _launcher_profile():
    try:
        home = Path.home()
    except Exception:
        return ""
    openomsi = home / ".openomsi"
    try:
        files = list(openomsi.iterdir())
    except OSError:
        return ""
    for f in files:
        if f.is_dir() or not f.suffix.lower() == ".json":
            continue
        low = f.name.lower()
        if (("config" not in low and "launcher" not in low)
                or "server" in low):
            continue
        try:
            if f.stat().st_size > 1 << 20:
                continue
            doc = json.loads(f.read_text(encoding="utf-8"))
        except Exception:
            continue
        if isinstance(doc, dict):
            for k, v in doc.items():
                if (isinstance(v, str) and k.strip().lower() in _PROFILE_KEYS
                        and _usable_name(v)):
                    return v.strip()
    return ""

def _newest_odr(game_dirs):
    best, best_mtime = "", -1.0
    for gd in game_dirs:
        if not gd:
            continue
        try:
            kids = list(Path(gd).iterdir())
        except OSError:
            continue
        drivers = next((k for k in kids
                        if k.is_dir() and k.name.lower() == "drivers"), None)
        if drivers is None:
            continue
        try:
            entries = list(drivers.iterdir())
        except OSError:
            continue
        for e in entries:
            if e.is_dir() or e.suffix.lower() != ".odr":
                continue
            stem = _usable_name(e.stem)
            if not stem:
                continue
            try:
                mt = e.stat().st_mtime
            except OSError:
                continue
            if (mt, stem.lower()) > (best_mtime, best.lower()):
                best, best_mtime = stem, mt
    return best

def _account_name():
    for var in ("USERNAME", "USER"):
        name = _usable_name(os.environ.get(var, ""))
        if name:
            return name
    try:
        import getpass
        return _usable_name(getpass.getuser())
    except Exception:
        return ""

def resolve_driver(pinned, game_dirs):
    """Return (name, source). Empty name means: ask the user."""
    if _usable_name(pinned):
        return pinned.strip(), "flag"
    prof = _launcher_profile()
    if prof:
        return prof, "launcher"
    odr = _newest_odr(game_dirs)
    if odr:
        return odr, "personnel-file"
    acc = _account_name()
    if acc:
        return acc, "account"
    return "", ""


# ------------------------------------------------------------------ posting
def post_live(panel_url: str, token: str, payload: dict, timeout: int = 8):
    data = json.dumps(payload).encode()
    req = urllib.request.Request(
        panel_url.rstrip("/") + "/api/live", data=data, method="POST",
        headers={"Content-Type": "application/json",
                 "User-Agent": "omsi-live-tracker-bridge/1.0"})
    if token:
        req.add_header("X-Live-Token", token)
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.status, r.read().decode(errors="replace")[:500]


def main():
    ap = argparse.ArgumentParser(description="openOMSI live-map bridge")
    ap.add_argument("--panel", default=os.environ.get("LIVE_PANEL", ""),
                    help="panel base URL, e.g. https://panel.example.com")
    ap.add_argument("--driver", default=os.environ.get("LIVE_DRIVER", ""),
                    help="pin one in-game name (default: auto-detect it, re-checked every post)")
    ap.add_argument("--game-dir", default=os.environ.get("GAME_DIR", ""),
                    help="game folder holding Drivers/ (default: this script's folder)")
    ap.add_argument("--token", default=os.environ.get("LIVE_TOKEN", ""),
                    help="live_token from the server's server.cfg (if set)")
    ap.add_argument("--omsi-root", default=os.environ.get("OMSI_ROOT", ""),
                    help="OMSI 2 folder (for bus capacity/spec lookup)")
    ap.add_argument("--port", type=int, default=UDP_PORT)
    ap.add_argument("--interval", type=float, default=2.0,
                    help="minimum seconds between POSTs")
    ap.add_argument("--debug", action="store_true",
                    help="verbose diagnostics: every datagram, POST and reply")
    args = ap.parse_args()
    DBG[0] = args.debug

    if not args.panel:
        print("error: --panel is required (or env LIVE_PANEL)", file=sys.stderr)
        return 2
    here = str(Path(__file__).resolve().parent)
    game_dirs = []
    for d in (args.game_dir, here, os.getcwd()):
        if d and d not in game_dirs:
            game_dirs.append(d)
    driver, source = resolve_driver(args.driver, game_dirs)
    if not driver:
        try:
            driver = input("Could not find your driver name. "
                           "Type your exact in-game driver name: ").strip()
            source = "typed"
        except (EOFError, KeyboardInterrupt):
            driver = ""
    if not driver:
        print("error: driver name is required (it links your reports "
              "to your bus on the map)", file=sys.stderr)
        return 2

    index = {}
    if args.omsi_root:
        root = Path(args.omsi_root)
        print(f"[bridge] indexing buses under {root} ...")
        index = scan_buses(root)
        print(f"[bridge] indexed {len(index)} bus entries")
    else:
        print("[bridge] no --omsi-root given: capacity/spec lookup disabled "
              "(position + passengers still reported)")

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("127.0.0.1", args.port))
    sock.settimeout(1.0)
    print(f"[bridge] driver={driver!r} (from {source}) panel={args.panel} "
          f"listening on 127.0.0.1:{args.port}")
    print(f"[bridge] token={'set (len %d)' % len(args.token) if args.token else 'NOT SET'} "
          f"omsi_root={args.omsi_root or '(none)'} debug={args.debug}")
    print("[bridge] start driving in openOMSI - reports forward automatically. "
          "Ctrl+C to stop.")

    last_post = 0.0
    pending = None
    failures = 0
    while True:
        try:
            try:
                data, _ = sock.recvfrom(MAX_DATAGRAM)
                dlog(f"datagram: {len(data)} bytes")
                try:
                    pending = json.loads(data.decode("utf-8"))
                    dlog(f"parsed keys: {sorted(pending.keys())} "
                         f"pax={pending.get('passengers')} "
                         f"fleet={pending.get('fleet_number')} "
                         f"reg={pending.get('reg_plate')} "
                         f"x={pending.get('x')} y={pending.get('y')}")
                except (UnicodeDecodeError, json.JSONDecodeError) as e:
                    print(f"[bridge] dropping undecodable datagram: {e}",
                          file=sys.stderr)
                    continue
            except socket.timeout:
                pass
            now = time.time()
            if pending is not None and now - last_post >= args.interval:
                payload = dict(pending)
                now_name, now_source = resolve_driver(args.driver, game_dirs)
                if now_name:
                    if now_name != driver:
                        print(f"[bridge] driver is now {now_name!r} "
                              f"(was {driver!r}, from {now_source})")
                        driver = now_name
                payload["driver"] = driver
                payload.setdefault("vehicle_manufacturer",
                                   payload.get("bus_manufacturer"))
                spec = resolve_bus(index, payload.get("vehicle_manufacturer"),
                                   payload.get("vehicle_model"))
                dlog(f"bus resolve: man={payload.get('vehicle_manufacturer')!r} "
                     f"model={payload.get('vehicle_model')!r} -> {spec}")
                payload.update({k: v for k, v in spec.items()
                                if k not in payload or payload[k] in (None, "")})
                try:
                    dlog(f"POST {args.panel.rstrip('/')}/api/live "
                         f"driver={payload.get('driver')!r} bytes={len(json.dumps(payload))}")
                    status, body = post_live(args.panel, args.token, payload)
                    dlog(f"reply: HTTP {status} {body[:200]}")
                    last_post = now
                    pending = None
                    failures = 0
                    print(f"[bridge] posted ({status}) "
                          f"bus={payload.get('vehicle_model') or '?'} "
                          f"pax={payload.get('passengers', '?')} "
                          f"x={payload.get('x', '?')} y={payload.get('y', '?')}",
                          flush=True)
                except Exception as e:  # panel down - keep latest, retry
                    failures += 1
                    if failures % 10 == 1:
                        print(f"[bridge] POST failed ({e}); retrying...",
                              file=sys.stderr)
                    time.sleep(min(5.0, failures))
        except KeyboardInterrupt:
            print("\n[bridge] stopped.")
            return 0


if __name__ == "__main__":
    sys.exit(main())
