// live_tracker_bridge.go - zero-dependency live-map forwarder for openOMSI.
//
// The game's live_tracker.lua can only send UDP to its own PC (127.0.0.1),
// so this program runs on the SAME pc as the game: it listens on
// 127.0.0.1:47800 and POSTs each report to the admin panel:
//
//	game (Lua) --UDP 127.0.0.1:47800--> bridge --HTTPS--> panel POST /api/live
//
// Bus type/spec/capacity are resolved panel-side; this only stamps the
// driver name (the Lua API does not expose the multiplayer player name,
// so it must match the in-game name for map merging) and forwards.
//
// Usage (double-click the .exe, or from a terminal):
//
//	live_tracker_bridge.exe -panel https://panel.example.com -driver "Your Name"
//	live_tracker_bridge.exe -panel https://panel.example.com   (asks for the name)
//	live_tracker_bridge.exe -panel ... -driver ... -token <live_token> -debug
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
	"strings"
	"time"
)

func main() {
	panel := flag.String("panel", "", "panel base URL, e.g. https://panel.example.com")
	driver := flag.String("driver", "", "in-game multiplayer name (asked when empty)")
	token := flag.String("token", "", "live_token from server.cfg (only if the server set one)")
	port := flag.Int("port", 47800, "UDP port the Lua plugin sends to")
	interval := flag.Float64("interval", 2.0, "minimum seconds between POSTs")
	debug := flag.Bool("debug", false, "verbose diagnostics")
	flag.Parse()

	if *panel == "" {
		fmt.Fprintln(os.Stderr, "error: -panel is required, e.g. -panel https://panel.example.com")
		os.Exit(2)
	}
	name := strings.TrimSpace(*driver)
	if name == "" {
		fmt.Print("Enter your exact in-game driver name: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		name = strings.TrimSpace(line)
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
	fmt.Printf("[bridge] driver=%q panel=%s listening on %s\n", name, *panel, addr)
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
