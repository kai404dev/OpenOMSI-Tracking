// live_tracker_bridge.go - zero-dependency live-map forwarder for openOMSI.
//
// The game's live_tracker.lua can only send UDP to its own PC (127.0.0.1),
// so this program runs on the SAME pc as the game: it listens on
// 127.0.0.1:47800 and POSTs each report to the admin panel:
//
//	game (Lua) --UDP 127.0.0.1:47800--> bridge --HTTPS--> panel POST /api/live
//
// Bus type/spec/capacity are resolved panel-side; this only stamps the
// driver name, which it finds by itself (the Lua API cannot see the
// multiplayer player name): the launcher's active profile, else the newest
// Drivers/*.odr personnel file beside the game, else the computer account.
// -driver pins one name instead. The name is re-checked on every post, so
// switching driver profile mid-session just works.
//
// Usage (double-click the .exe, or from a terminal):
//
//	live_tracker_bridge.exe -panel https://panel.example.com
//	live_tracker_bridge.exe -panel ... -driver "Name" -token <live_token> -debug
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The game decides the multiplayer name as: --lan-name (the launcher passes
// the driver profile's name), else the personnel file's name, else the
// computer account's (see lan::player_name in openOMSI). Lua cannot see it,
// so the bridge reads it from disk with the same order, re-checked on every
// post so profile switches apply without restarting the bridge:
//  1. -driver flag (an explicit pin, always wins)
//  2. the launcher's active profile in ~/.openomsi (exact when found)
//  3. the newest Drivers/*.odr personnel file beside the game
//     (the game rewrites it every session; bare "Driver" is skipped like
//     the game skips it)
//  4. the computer account (USERNAME on Windows, USER elsewhere)
//  5. ask once on the terminal (last resort)
func resolveDriver(pinned, gameDir string) (name, source string) {
	if strings.TrimSpace(pinned) != "" {
		return strings.TrimSpace(pinned), "flag"
	}
	if p := launcherProfile(); p != "" {
		return p, "launcher"
	}
	if o := newestOdr(gameDir); o != "" {
		return o, "personnel-file"
	}
	if u := strings.TrimSpace(os.Getenv("USERNAME")); u != "" {
		return u, "account"
	}
	if u := strings.TrimSpace(os.Getenv("USER")); u != "" {
		return u, "account"
	}
	return "", ""
}

// launcherProfile reads the active driver profile from the launcher's own
// files in ~/.openomsi. Only keys that name a profile count, so the server
// list (servers.json) can never leak in as a driver name.
func launcherProfile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".openomsi")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	wantFile := func(n string) bool {
		l := strings.ToLower(n)
		return strings.HasSuffix(l, ".json") &&
			(strings.Contains(l, "config") || strings.Contains(l, "launcher")) &&
			!strings.Contains(l, "server")
	}
	wantKey := func(k string) bool {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "profile", "driver_profile", "active_profile", "driver",
			"player", "player_name", "lan_name", "pilot":
			return true
		}
		return false
	}
	for _, e := range entries {
		if e.IsDir() || !wantFile(e.Name()) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil || len(raw) > 1<<20 {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			continue
		}
		for k, v := range doc {
			s, ok := v.(string)
			if ok && wantKey(k) && usableName(s) {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func usableName(s string) bool {
	t := strings.TrimSpace(s)
	return t != "" && !strings.EqualFold(t, "Driver")
}

// newestOdr returns the stem of the most recently written Drivers/*.odr
// personnel file under dir (case-insensitive folder match).
func newestOdr(dir string) string {
	if dir == "" {
		return ""
	}
	drivers := ""
	kids, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, k := range kids {
		if k.IsDir() && strings.EqualFold(k.Name(), "Drivers") {
			drivers = filepath.Join(dir, k.Name())
			break
		}
	}
	if drivers == "" {
		return ""
	}
	entries, err := os.ReadDir(drivers)
	if err != nil {
		return ""
	}
	var best string
	var bestTime time.Time
	found := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if len(n) < 5 || !strings.EqualFold(n[len(n)-4:], ".odr") {
			continue
		}
		stem := strings.TrimSpace(n[:len(n)-4])
		if !usableName(stem) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		mt := info.ModTime()
		if !found || mt.After(bestTime) || (mt.Equal(bestTime) && stem < best) {
			best, bestTime, found = stem, mt, true
		}
	}
	return best
}

func main() {
	panel := flag.String("panel", "", "panel base URL, e.g. https://panel.example.com")
	driver := flag.String("driver", "", "pin one in-game name (default: auto-detect it, re-checked every post)")
	token := flag.String("token", "", "live_token from server.cfg (only if the server set one)")
	gameDir := flag.String("game-dir", "", "game folder holding Drivers/ (default: this program's folder)")
	port := flag.Int("port", 47800, "UDP port the Lua plugin sends to")
	interval := flag.Float64("interval", 2.0, "minimum seconds between POSTs")
	debug := flag.Bool("debug", false, "verbose diagnostics")
	flag.Parse()

	if *panel == "" {
		fmt.Fprintln(os.Stderr, "error: -panel is required, e.g. -panel https://panel.example.com")
		os.Exit(2)
	}
	exeDir := ""
	if ex, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(ex)
	}
	// candidate game folders for the personnel-file lookup
	gameDirs := []string{}
	seenDir := map[string]bool{}
	for _, d := range []string{*gameDir, exeDir} {
		if d != "" && !seenDir[d] {
			seenDir[d] = true
			gameDirs = append(gameDirs, d)
		}
	}
	if cwd, err := os.Getwd(); err == nil && !seenDir[cwd] {
		gameDirs = append(gameDirs, cwd)
	}
	currentName := func() (string, string) {
		if strings.TrimSpace(*driver) != "" {
			return strings.TrimSpace(*driver), "flag"
		}
		if p := launcherProfile(); p != "" {
			return p, "launcher"
		}
		for _, d := range gameDirs {
			if o := newestOdr(d); o != "" {
				return o, "personnel-file"
			}
		}
		if u := strings.TrimSpace(os.Getenv("USERNAME")); u != "" {
			return u, "account"
		}
		if u := strings.TrimSpace(os.Getenv("USER")); u != "" {
			return u, "account"
		}
		return "", ""
	}
	name, source := currentName()
	if name == "" {
		fmt.Print("Could not find your driver name. Type your exact in-game driver name: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		name = strings.TrimSpace(line)
		source = "typed"
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "error: driver name is required (it links your reports to your bus on the map)")
		os.Exit(2)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot listen on %s: %v (another bridge already running?)\n", addr, err)
		os.Exit(1)
	}
	defer conn.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	postURL := strings.TrimRight(*panel, "/") + "/api/live"
	fmt.Printf("[bridge] driver=%q (from %s) panel=%s listening on %s\n", name, source, *panel, addr)
	fmt.Printf("[bridge] token=%s debug=%v\n", map[bool]string{true: "set", false: "NOT SET"}[*token != ""], *debug)
	fmt.Println("[bridge] drive in openOMSI - reports forward automatically. Close this window to stop.")

	buf := make([]byte, 65535)
	var pending map[string]any
	var haveReport bool
	lastPost := time.Now().Add(-time.Hour)
	failures := 0

	flush := func(force bool) {
		if !haveReport {
			return
		}
		if !force && time.Since(lastPost) < time.Duration(*interval*float64(time.Second)) {
			return
		}
		// re-check the name on every post: switching driver profile
		// mid-session just works, no restart asked.
		nowName, nowSource := currentName()
		if nowName == "" {
			nowName = name // keep the last good one rather than nothing
		} else if nowName != name {
			fmt.Printf("[bridge] driver is now %q (was %q, from %s)\n", nowName, name, nowSource)
			name = nowName
		}
		payload := make(map[string]any, len(pending)+1)
		for k, v := range pending {
			payload[k] = v
		}
		payload["driver"] = name
		body, _ := json.Marshal(payload)
		if *debug {
			fmt.Printf("[bridge:dbg] POST %s driver=%q bytes=%d pax=%v fleet=%v reg=%v x=%v y=%v\n",
				postURL, name, len(body), payload["passengers"], payload["fleet_number"], payload["reg_plate"], payload["x"], payload["y"])
		}
		req, _ := http.NewRequest("POST", postURL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "omsi-live-tracker-bridge/1.0")
		if *token != "" {
			req.Header.Set("X-Live-Token", *token)
		}
		resp, err := client.Do(req)
		if err != nil {
			failures++
			if failures%10 == 1 {
				fmt.Fprintf(os.Stderr, "[bridge] POST failed (%v); retrying...\n", err)
			}
			time.Sleep(time.Duration(min(failures, 5)) * time.Second)
			return
		}
		resp.Body.Close()
		lastPost = time.Now()
		haveReport = false
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			failures++
			fmt.Fprintf(os.Stderr, "[bridge] panel said HTTP %d (wrong -token? wrong -panel?)\n", resp.StatusCode)
			return
		}
		failures = 0
		fmt.Printf("[bridge] posted (%d) bus=%v pax=%v x=%v y=%v\n",
			resp.StatusCode, payload["vehicle_model"], payload["passengers"], payload["x"], payload["y"])
	}

	for {
		conn.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			flush(false)
			continue
		}
		if *debug {
			fmt.Printf("[bridge:dbg] datagram: %d bytes\n", n)
		}
		var rep map[string]any
		if err := json.Unmarshal(buf[:n], &rep); err != nil {
			fmt.Fprintf(os.Stderr, "[bridge] dropping undecodable datagram: %v\n", err)
			continue
		}
		if *debug {
			keys := make([]string, 0, len(rep))
			for k := range rep {
				keys = append(keys, k)
			}
			fmt.Printf("[bridge:dbg] keys=%v pax=%v fleet=%v reg=%v\n", keys, rep["passengers"], rep["fleet_number"], rep["reg_plate"])
		}
		pending = rep
		haveReport = true
		flush(false)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
