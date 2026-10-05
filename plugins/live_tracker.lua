-- plugins/live_tracker.lua
-- openOMSI live-fleet tracker (client side).
--
-- Every REPORT_EVERY seconds it gathers everything the admin panel's live map
-- needs about this player and hands it to the companion bridge on this same PC:
--
--   * map position: x, y, z (map metres), heading, tile + in-tile offset
--   * passengers aboard (omsi.info().passengers) and speed
--   * bus identity: manufacturer, model, full name (bus type + spec source)
--   * service: line, tour, trip, terminus/destination, next stop + arrival
--
-- Transport: omsi.send() can only talk to 127.0.0.1, never over the network,
-- so this plugin sends one JSON datagram per report to 127.0.0.1:47800 where
-- live_tracker_bridge.py listens and POSTs it to the admin panel's
-- POST /api/live. Max capacity (seats/standing) is resolved bridge-side by
-- counting [passpos] entries in the bus's passengercabin cfg, because Lua
-- plugins have no file access.
--
-- Install: copy this file to <game>/plugins/live_tracker.lua (next to the
-- game, same folder the hello.lua example uses). No compiler needed; editing
-- it while the game runs reloads it within a second.
-- Then run the bridge on the same PC (see ../bridge/live_tracker_bridge.py).
-- The driver name shown on the live map is configured in the bridge, because
-- the Lua API does not expose the multiplayer player name.

local PORT = 47800
local REPORT_EVERY = 2 -- seconds between reports
local DATAGRAM_LIMIT = 8000 -- omsi.send refuses messages longer than 8 KB
local DEBUG = true -- verbose game.log diagnostics (flip to false when healthy;
                   -- the file reloads live when saved, no restart needed)

local attempts = 0 -- send attempts this session (resets on reload)
local frames = 0 -- game frames seen this session

-- omsi.send arrived in openOMSI 0.1.1668; older games load this plugin
-- fine but cannot talk to the bridge. Detect it once and say so plainly
-- instead of erroring on every report until the game switches us off.
local HAS_SEND = type(omsi.send) == "function"

local function dbg(msg)
  if DEBUG then
    omsi.log("live_tracker [dbg] " .. tostring(msg))
  end
end

omsi.data.reports_sent = omsi.data.reports_sent or 0

-- Minimal JSON encoder: numbers, booleans, strings, nil, flat tables only.
-- (Nested tables are encoded one level deep, which is all we send.)
local function json_escape(s)
  s = tostring(s)
  s = s:gsub("\\", "\\\\")
  s = s:gsub('"', '\\"')
  s = s:gsub("\n", "\\n")
  s = s:gsub("\r", "\\r")
  s = s:gsub("\t", "\\t")
  return '"' .. s .. '"'
end

local function json_value(v)
  local t = type(v)
  if t == "string" then
    return json_escape(v)
  elseif t == "number" then
    if v ~= v or v == math.huge or v == -math.huge then
      return "null"
    end
    return tostring(v)
  elseif t == "boolean" then
    return v and "true" or "false"
  else
    return "null"
  end
end

local function json_encode(tbl)
  local parts = {}
  for k, v in pairs(tbl) do
    if type(k) == "string" and v ~= nil then
      parts[#parts + 1] = json_escape(k) .. ":" .. json_value(v)
    end
  end
  return "{" .. table.concat(parts, ",") .. "}"
end

local function num(v)
  if type(v) == "number" and v == v and v ~= math.huge and v ~= -math.huge then
    return v
  end
  return nil
end

-- A few buses expose extra detail as script variables; most expose none of
-- these, so every lookup is optional and missing values are simply omitted.
local EXTRA_VARS = {
  "bus_doorfront0", "bus_doorfront1", "bus_doorback0", "bus_doorback1",
  "bus_stop_brake", "IBIS_line", "IBIS_tour",
}

local function collect()
  local i = omsi.info()
  local report = {}

  report.v = 1 -- payload schema version
  report.client_time = os.time()

  if i then
    report.map = i.map
    report.clock = i.clock
    report.date = i.day and (tostring(i.year or "") .. "-" .. tostring(i.day or "")) or nil
    report.view = i.view
    report.on_foot = i.on_foot
    report.paused = i.paused
    report.multiplayer = i.multiplayer
    report.speed_kmh = num(i.speed)
    report.passengers = num(i.passengers)
    report.tile_x = num(i.tile_x)
    report.tile_y = num(i.tile_y)
    report.tile_pos_x = num(i.tile_pos_x)
    report.tile_pos_y = num(i.tile_pos_y)
    report.heading = num(i.heading)
    report.vehicle_manufacturer = i.vehicle_manufacturer
    report.vehicle_model = i.vehicle_model
    report.destination = i.destination
    report.line = i.line
    report.tour = i.tour
    report.trip = num(i.trip)
    report.trips = num(i.trips)
    report.trip_name = i.trip_name
    report.terminus = i.terminus
    report.next_stop = i.next_stop
    report.next_stop_number = num(i.next_stop_number)
    report.next_stop_arrival = num(i.next_stop_arrival)
    report.next_stop_departure = num(i.next_stop_departure)
    report.delay = num(i.delay)
  end

  if omsi.has_vehicle and omsi.has_vehicle() then
    report.has_vehicle = true
    report.vehicle = omsi.vehicle()
    if omsi.vehicle_manufacturer then
      report.vehicle_manufacturer = report.vehicle_manufacturer or omsi.vehicle_manufacturer()
    end
    if omsi.vehicle_model then
      report.vehicle_model = report.vehicle_model or omsi.vehicle_model()
    end
  else
    report.has_vehicle = false
  end

  local x, y, z, heading = omsi.position()
  if x then
    report.x = num(x)
    report.y = num(y)
    report.z = num(z)
    report.heading = report.heading or num(heading)
  end

  -- Optional bus detail, best effort: doors / brake / IBIS mirrors.
  for _, name in ipairs(EXTRA_VARS) do
    local ok, value = pcall(omsi.var, name)
    if ok and type(value) == "number" then
      report["var_" .. name] = value
    end
  end
  if omsi.str then
    local ok, s = pcall(omsi.str, "IBIS_busstop_name")
    if ok and type(s) == "string" and s ~= "" then
      report.ibis_busstop = s
    end
    local ok2, s2 = pcall(omsi.str, "IBIS_terminus_name")
    if ok2 and type(s2) == "string" and s2 ~= "" then
      report.ibis_terminus = s2
    end
    -- Fleet number and registration plate: openOMSI installs the chosen
    -- [number] entry and its plate as the bus's "number" / "ident" string
    -- variables before the scripts' first {init}, so they read like any
    -- other string variable. Absent on buses without a [number] list.
    local fleet_ok, fleet = pcall(omsi.str, "number")
    if fleet_ok and type(fleet) == "string" and fleet ~= "" then
      report.fleet_number = fleet
    end
    local reg_ok, reg = pcall(omsi.str, "ident")
    if reg_ok and type(reg) == "string" and reg ~= "" then
      report.reg_plate = reg
    end
    local yard_ok, yard = pcall(omsi.str, "yard")
    if yard_ok and type(yard) == "string" and yard ~= "" then
      report.depot = yard
    end
  end

  return report
end

local function send_report()
  attempts = attempts + 1
  if not HAS_SEND then
    return -- old game: on_start already said to update
  end
  local ok, report = pcall(collect)
  if not ok or type(report) ~= "table" then
    omsi.warn("live_tracker: collect FAILED on attempt #" .. attempts .. ": " .. tostring(report))
    return
  end
  local payload = json_encode(report)
  dbg("attempt #" .. attempts .. ": bytes=" .. #payload
    .. " has_vehicle=" .. tostring(report.has_vehicle)
    .. " x=" .. tostring(report.x) .. " y=" .. tostring(report.y)
    .. " speed=" .. tostring(report.speed_kmh)
    .. " pax=" .. tostring(report.passengers)
    .. " fleet=" .. tostring(report.fleet_number)
    .. " reg=" .. tostring(report.reg_plate))
  if #payload > DATAGRAM_LIMIT then
    omsi.warn("live_tracker: report too large, dropped (" .. #payload .. " bytes)")
    return
  end
  local sent, reason = omsi.send(PORT, payload)
  if sent then
    omsi.data.reports_sent = (omsi.data.reports_sent or 0) + 1
    if DEBUG and omsi.data.reports_sent <= 3 then
      dbg("report #" .. tostring(omsi.data.reports_sent) .. " SENT ok (" .. #payload .. " bytes to 127.0.0.1:" .. PORT .. ")")
    end
  else
    -- Always log the first failures loudly (this is the #1 setup problem:
    -- usually the bridge is not running). Then throttle to avoid spam.
    if attempts <= 5 or attempts % 30 == 0 then
      omsi.log("live_tracker: send FAILED on attempt #" .. attempts
        .. " to 127.0.0.1:" .. PORT .. " (" .. tostring(reason)
        .. ") - is live_tracker_bridge.py running on THIS pc?")
    end
  end
end

function on_start()
  omsi.log("live_tracker: started, reporting to 127.0.0.1:" .. PORT .. " every " .. REPORT_EVERY .. "s")
  if not HAS_SEND then
    omsi.warn("live_tracker: this game has no omsi.send - update openOMSI to 0.1.1668 or newer, then live reporting will start by itself")
    return
  end
  dbg("game version=" .. tostring(omsi.version) .. " DEBUG=" .. tostring(DEBUG))
  local hv = omsi.has_vehicle and omsi.has_vehicle()
  dbg("has_vehicle=" .. tostring(hv)
    .. " vehicle=" .. tostring(omsi.vehicle and omsi.vehicle())
    .. " manufacturer=" .. tostring(omsi.vehicle_manufacturer and omsi.vehicle_manufacturer())
    .. " model=" .. tostring(omsi.vehicle_model and omsi.vehicle_model()))
  local ok, i = pcall(omsi.info)
  if ok and type(i) == "table" then
    local n = 0
    for _ in pairs(i) do n = n + 1 end
    dbg("info(): keys=" .. n .. " map=" .. tostring(i.map)
      .. " passengers=" .. tostring(i.passengers) .. " speed=" .. tostring(i.speed)
      .. " paused=" .. tostring(i.paused) .. " on_foot=" .. tostring(i.on_foot)
      .. " multiplayer=" .. tostring(i.multiplayer) .. " view=" .. tostring(i.view))
  else
    omsi.warn("live_tracker: info() FAILED: " .. tostring(i))
  end
  local x, y, z, h = omsi.position()
  dbg("position(): x=" .. tostring(x) .. " y=" .. tostring(y) .. " z=" .. tostring(z) .. " heading=" .. tostring(h))
  if omsi.str then
    local _, fleet = pcall(omsi.str, "number")
    local _, reg = pcall(omsi.str, "ident")
    local _, yard = pcall(omsi.str, "yard")
    dbg('str vars: number=' .. tostring(fleet) .. ' ident=' .. tostring(reg) .. ' yard=' .. tostring(yard))
  else
    dbg("omsi.str MISSING (string variables unreadable)")
  end
end

-- Frame heartbeat: proves the game's timer queue is reaching this plugin.
-- Logs once on the first frame, then every ~30s. Cheap: a counter only.
function on_frame(dt)
  frames = frames + 1
  if DEBUG and frames == 1 then
    dbg("first game frame seen, dt=" .. tostring(dt) .. " - timers are ticking")
  end
  if DEBUG and frames % 1800 == 0 then
    local i = omsi.info()
    dbg("heartbeat: frames=" .. frames
      .. " attempts=" .. attempts
      .. " reports_sent=" .. tostring(omsi.data.reports_sent or 0)
      .. " paused=" .. tostring(i and i.paused)
      .. " on_foot=" .. tostring(i and i.on_foot))
  end
end

omsi.every(REPORT_EVERY, send_report)

function on_stop()
  omsi.log("live_tracker: stopped after " .. tostring(omsi.data.reports_sent or 0)
    .. " reports (" .. attempts .. " attempts, " .. frames .. " frames this session)")
end
