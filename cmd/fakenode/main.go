// fakenode is a stand-in LumiNode for testing the Configure page without hardware.
// It serves the subset of the Luminex WebAPI the app uses, on the Mac's LAN
// address (port 8081 by default), and advertises itself over mDNS with
// Bonjour's dns-sd so discovery finds it. Run the app with DEVICE_PORT=8081 so
// it talks to this port instead of 80.
//
// Its data follows what a real LumiNode 12 (firmware v2.9.1) sends, see
// docs/samples: sACN/Art-Net IOs have small ids, DMX port IOs are
// 100000+port and named "Port n", internal IOs are 101000+pb, and every
// block carries mode, summarized_active_input, sources, colors, the *_ctrl
// ids and a startup_buffer. --samples <dir> serves those captured files
// verbatim instead of the built-in seed.
//
// It also answers Art-Net RDM for fake fixtures on DMX ports 1 and 2 (see
// rdm.go) on UDP --artnet-port (6455, as the app holds 6454 on the same
// computer); run the app with DEVICE_ARTNET_PORT=6455 too.
//
//	go run ./cmd/fakenode [--port 8081] [--artnet-port 6455] [--ip a.b.c.d] [--serial S] [--name N] [--ports 4] [--no-mdns] [--samples docs/samples] [--password P]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type obj = map[string]any

var (
	mu      sync.Mutex
	started = time.Now()
	downTil time.Time
	ios     []obj
	pbs     []obj
	nextIO  int
	nPorts  int
	info    obj
	ipset   obj
	dmxCfg  = obj{"framerate": 40, "break_time": 176, "output_time": 0, "rdm_controller_ip": "0.0.0.0"}
	frames  int
)

func lanIP() string {
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
				return ipn.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

// The real-node id namespaces (LumiNode 12, v2.9.1).
const (
	dmxIOBase      = 100000 // DMX port IOs: dmxIOBase + port
	internalIOBase = 101000 // internal IOs: internalIOBase + process block
)

func defaultPB(id int) obj {
	return obj{"id": id, "name": fmt.Sprintf("Process Engine %d", id+1), "mode": "NONE", "inputs": obj{}, "outputs": obj{},
		"sources": obj{"inputs": []any{nil, nil, nil, nil}}, "patches": obj{"0": []any{}, "1": []any{}, "2": []any{}, "3": []any{}},
		"backup_auto_recover": true, "backup_ctrl": -1, "backup_ctrl_patch": []any{}, "colors": []any{"#00ffa1"}, "masterlimit": "NONE",
		"masterlimit_ctrl": -1, "masterlimit_ctrl_patch": []any{}, "multi_channel_definitions": []any{}, "ratelimiting": true,
		"startup_buffer": []any{}, "switch_ctrl": -1, "switch_ctrl_patch": []any{}, "timeout": 0, "xfade_ctrl": -1, "xfade_ctrl_patch": []any{},
		"summarized_active_input": nil, "takeover_active": false, "status": obj{"output": "$subscription-location"}}
}

// patchRefusal is the 422 a real LumiNode 12 (v2.9.1) gives a PATCH without
// inputs and outputs: PATCH replaces a block, it does not edit one.
func patchRefusal(b obj) string {
	for _, k := range []string{"inputs", "outputs"} {
		if _, ok := b[k]; !ok {
			return "Failed to deserialize data from JSON: missing field `" + k + "`"
		}
	}
	return ""
}

// ctrlFields maps a control type, which is also its IO's io_type, to its block field.
var ctrlFields = map[string]string{"backup-ctrl": "backup_ctrl", "switch-ctrl": "switch_ctrl", "x-fade-ctrl": "xfade_ctrl", "masterlimit-ctrl": "masterlimit_ctrl"}

// spreadNewControl does what a real LumiNode 12 (v2.9.1) does when a control IO is
// created: it attaches it to every block with that control unset.
func spreadNewControl(io obj) {
	field, ok := ctrlFields[fmt.Sprint(io["io_type"])]
	if !ok {
		return
	}
	for _, p := range pbs {
		if toF(p[field]) < 0 {
			p[field] = int(toF(io["id"]))
		}
	}
}

// modeSlots is how many inputs a real LumiNode 12 (v2.9.1) keeps in each mode.
var modeSlots = map[string]int{"REROUTE": 1, "BACKUP": 2, "XFADE": 2, "SWITCH": 4, "HTP": 4, "LTP": 4, "CUSTOM": 4}

func modeSlotsOr(mode any) int {
	if limit, ok := modeSlots[fmt.Sprint(mode)]; ok {
		return limit
	}
	return 4
}

// updateBlock does what a real LumiNode 12 (v2.9.1) does with a PUT: nothing
// without a mode (seen on a NONE block); with one, fields left out return to their
// defaults, inputs and outputs stay, and mode NONE clears the block and deletes
// any sACN or Art-Net IO that leaves unused; a mode with fewer slots drops the rest.
func updateBlock(i int, b obj) {
	mode, ok := b["mode"]
	if !ok {
		log.Printf("  PUT PB%d without a mode: changes nothing", i)
		return
	}
	if mode == "NONE" {
		pbs[i] = defaultPB(i)
		deleteUnusedNetworkIOs()
		return
	}
	if limit, ok := modeSlots[fmt.Sprint(mode)]; ok {
		in := pbs[i]["inputs"].(obj)
		for slot := range in {
			if n, _ := strconv.Atoi(slot); n >= limit {
				delete(in, slot)
			}
		}
	}
	defaults := obj{"name": "", "timeout": 1000, "backup_auto_recover": true, "ratelimiting": true, "colors": []any{"#00ffa1"},
		"masterlimit": "NONE", "startup_buffer": []any{}, "multi_channel_definitions": []any{}}
	pbs[i]["mode"] = mode
	for k, def := range defaults {
		if v, ok := b[k]; ok {
			pbs[i][k] = v
		} else {
			pbs[i][k] = def
		}
	}
}

// ioAttached says whether some block already uses this IO: a real node tracks IO usage
// across blocks and refuses to attach one twice.
func ioAttached(id int) bool { return attachedIOs()[id] }

func deleteUnusedNetworkIOs() {
	used := attachedIOs()
	kept := ios[:0]
	for _, io := range ios {
		if (io["io_class"] == "sacn" || io["io_class"] == "artnet") && !used[int(toF(io["id"]))] {
			log.Printf("  IO %v left unused: deleted", io["id"])
			continue
		}
		kept = append(kept, io)
	}
	ios = kept
}

func attachedIOs() map[int]bool {
	used := map[int]bool{}
	for _, p := range pbs {
		for _, side := range []string{"inputs", "outputs"} {
			if m, ok := p[side].(obj); ok {
				for _, v := range m {
					used[int(toF(v))] = true
				}
			}
		}
		for _, ctrl := range []string{"backup_ctrl", "switch_ctrl", "xfade_ctrl", "masterlimit_ctrl"} {
			if v, ok := p[ctrl]; ok {
				used[int(toF(v))] = true
			}
		}
	}
	return used
}

func sacnInput(id, universe, priority int, name string) obj {
	return obj{"id": id, "name": name, "io_class": "sacn", "io_type": "input", "universe": universe, "priority": priority,
		"source_ip": "0.0.0.0", "group": 0, "take_own_ip": true, "per_channel_priority": false, "duplicate_ios": []any{}}
}

func artnetInput(id, universe int, name string) obj {
	return obj{"id": id, "name": name, "io_class": "artnet", "io_type": "input", "universe": universe,
		"source_ip": "0.0.0.0", "group": 0, "take_own_ip": true, "duplicate_ios": []any{}}
}

func dmxPortIO(port int, ioType string, rdm bool) obj {
	return obj{"id": dmxIOBase + port, "name": fmt.Sprintf("Port %d", port+1), "io_class": "dmx", "io_type": ioType, "port_number": port,
		"rdm": rdm, "rdm_universe": -1, "interweaving": true, "adaptive_discovery": false, "backup": false, "follow": false,
		"stream_activity_time": 6000, "stream_activity_time_custom": false}
}

// loadSamples replaces the seed with the raw captures in dir
// (<prefix>-deviceinfo.json, -IO.json, -processblock.json).
func loadSamples(dir string) error {
	matches, _ := filepath.Glob(filepath.Join(dir, "*-processblock.json"))
	if len(matches) == 0 {
		return fmt.Errorf("no *-processblock.json in %s", dir)
	}
	prefix := strings.TrimSuffix(matches[0], "-processblock.json")
	read := func(suffix string, v any) error {
		data, err := os.ReadFile(prefix + suffix)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, v)
	}
	if err := read("-processblock.json", &pbs); err != nil {
		return err
	}
	if err := read("-IO.json", &ios); err != nil {
		return err
	}
	if err := read("-deviceinfo.json", &info); err != nil {
		return err
	}
	nPorts = int(toF(info["nr_dmx_ports"]))
	nextIO = 0
	for _, io := range ios {
		if id := int(toF(io["id"])); id < dmxIOBase && id >= nextIO {
			nextIO = id + 1
		}
	}
	log.Printf("serving %s: %d process blocks, %d IOs, %d DMX ports", prefix, len(pbs), len(ios), nPorts)
	return nil
}

// seed builds a node with nPorts DMX ports and nPorts+4 process blocks (a
// LumiNode 12 has 12 ports and 16 blocks), wired the way a commissioned node
// looks: REROUTE blocks per port, one BACKUP, one HTP with a startup buffer.
func seed(serial, name string) {
	nPBs := nPorts + 4
	info = obj{"ID": 0, "OEM": 0, "type": fmt.Sprintf("LumiNode%d", nPorts), "nr_dmx_ports": nPorts, "nr_processblocks": nPBs, "mac_address": "00:50:c2:12:34:56",
		"serial": serial, "short_name": "LN" + strconv.Itoa(nPorts), "long_name": name, "default_ip": "2.0.1.1/8", "rdm_uid": 0, "colors": []any{},
		"auth": obj{"web_auth_enabled": false, "password": nil}, "contact_closure_present": false, "display_present": true,
		"features": obj{"networking: Maximum number of input interfaces": 1, "networking: Maximum number of output interfaces": 1, "networking: Split pipeline IO": false}}

	// Network inputs: sACN U1..Un (one per port, Quick Setup style), then a
	// named pair for the merge/backup blocks and an Art-Net one.
	ios = nil
	for p := 0; p < nPorts; p++ {
		ios = append(ios, sacnInput(p, p+1, 100, ""))
	}
	nextIO = nPorts
	ios = append(ios, sacnInput(nextIO, 101, 150, "Desk A main"))
	ios = append(ios, sacnInput(nextIO+1, 101, 100, "Desk B backup"))
	ios[nextIO]["duplicate_ios"] = []any{nextIO + 1} // same universe twice: the node flags both
	ios[nextIO+1]["duplicate_ios"] = []any{nextIO}
	ios = append(ios, artnetInput(nextIO+2, 0, "Media server"))
	ios[nextIO+2]["group"] = 2
	ios[nextIO+2]["take_own_ip"] = false
	nextIO += 3
	rich := nPorts >= 4 // room for the BACKUP and HTP blocks and an idle port
	for p := 0; p < nPorts; p++ {
		t := "output"
		if rich && p == nPorts-1 {
			t = "idle" // last port left unused
		}
		ios = append(ios, dmxPortIO(p, t, p == 0))
	}
	ios[dmxIndex(0)]["rdm_universe"] = 1
	for b := 0; b < nPBs; b++ {
		ios = append(ios, obj{"id": internalIOBase + b, "name": "", "io_class": "internal", "io_type": "input", "pb_number": b})
	}

	pbs = nil
	for b := 0; b < nPBs; b++ {
		pbs = append(pbs, defaultPB(b))
	}
	// Ports 1..n-3: plain REROUTE, as Quick Setup leaves them (every port
	// when there are fewer than four).
	plain := nPorts
	if rich {
		plain = nPorts - 3
	}
	for p := 0; p < plain; p++ {
		pbs[p]["name"], pbs[p]["mode"] = fmt.Sprintf("ProcessEngine %d", p+1), "REROUTE"
		pbs[p]["inputs"] = obj{"0": p}
		pbs[p]["outputs"] = obj{strconv.Itoa(dmxIOBase + p): dmxIOBase + p}
	}
	if !rich {
		return
	}
	// Port n-2: BACKUP, desk A live on slot 0, desk B standing by, switched by the Art-Net input.
	bk := nPorts - 3
	pbs[bk]["name"], pbs[bk]["mode"] = "FOH backup", "BACKUP"
	pbs[bk]["inputs"] = obj{"0": nPorts, "1": nPorts + 1}
	pbs[bk]["outputs"] = obj{strconv.Itoa(dmxIOBase + bk): dmxIOBase + bk}
	pbs[bk]["timeout"], pbs[bk]["backup_auto_recover"] = 2500, false
	pbs[bk]["backup_ctrl"] = nPorts + 2
	pbs[bk]["summarized_active_input"] = 0
	pbs[bk]["sources"] = obj{"inputs": []any{obj{"ip": "192.168.1.10", "name": "Desk A", "priority": 150, "group": nil}, nil, nil, nil}}
	pbs[bk]["colors"] = []any{"#8050d8"}
	// Port n-1: HTP merge of both desks, master limited, with a startup buffer.
	ht := nPorts - 2
	pbs[ht]["name"], pbs[ht]["mode"] = "Stage HTP", "HTP"
	pbs[ht]["inputs"] = obj{"0": nPorts, "1": nPorts + 1}
	pbs[ht]["outputs"] = obj{strconv.Itoa(dmxIOBase + ht): dmxIOBase + ht}
	pbs[ht]["masterlimit"], pbs[ht]["masterlimit_ctrl"] = "LIMIT", nPorts+2
	buf := make([]any, 512)
	for i := range buf {
		buf[i] = (i * 7) % 256
	}
	pbs[ht]["startup_buffer"] = buf
	pbs[ht]["colors"] = []any{"#e09820"}
	pbs[ht]["sources"] = obj{"inputs": []any{
		[]any{obj{"ip": "192.168.1.10", "name": "Desk A", "priority": 150}, obj{"ip": "192.168.1.11", "name": "Desk A tracking", "priority": 150}},
		obj{"ip": "192.168.1.20", "name": "Desk B", "priority": 100}, nil, nil}}
	// Port n: left NONE with the port idle. Blocks beyond the ports stay NONE.
}

// dmxIndex returns the position of DMX port p in ios (ports come after the network inputs).
func dmxIndex(p int) int {
	for i, io := range ios {
		if io["io_class"] == "dmx" && int(toF(io["port_number"])) == p {
			return i
		}
	}
	return -1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func bad(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, obj{"error": msg})
}

func body(r *http.Request) obj {
	var o obj
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &o)
	if o == nil {
		o = obj{}
	}
	return o
}

func idx(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

func findIO(id int) obj {
	for _, io := range ios {
		if int(toF(io["id"])) == id {
			return io
		}
	}
	return nil
}

func toF(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	}
	return -1
}

func pb(w http.ResponseWriter, s string) obj {
	i := idx(s)
	if i < 0 || i >= len(pbs) {
		bad(w, 404, "no such processblock")
		return nil
	}
	return pbs[i]
}

func main() {
	port := flag.Int("port", 8081, "HTTP port")
	artnetPort := flag.Int("artnet-port", 6455, "UDP port for Art-Net RDM (0: off)")
	ip := flag.String("ip", "", "address to listen on and advertise (default: LAN IP)")
	serial := flag.String("serial", "LUM452297270", "serial")
	name := flag.String("name", "Fake LumiNode 4", "long name")
	flag.IntVar(&nPorts, "ports", 4, "DMX ports")
	noMdns := flag.Bool("no-mdns", false, "don't register with dns-sd")
	samples := flag.String("samples", "", "serve the raw captures in this folder (docs/samples) instead of the built-in seed")
	password := flag.String("password", "", "require Basic admin:<password>")
	flag.DurationVar(&rdmSearch, "rdm-search", 0, "how long an RDM search takes (0: at once). Longer than 0, the first search after RDM goes on misses a fixture, as a real node's did")
	flag.Parse()
	if *ip == "" {
		*ip = lanIP()
	}
	seed(*serial, *name)
	if *samples != "" {
		if err := loadSamples(*samples); err != nil {
			log.Fatalf("--samples: %v", err)
		}
		*serial, _ = info["serial"].(string)
		*name, _ = info["long_name"].(string)
	}
	ipset = obj{"ipaddress": *ip, "netmask": "255.255.0.0", "gateway": "0.0.0.0", "broadcast": "0.0.0.0", "is_basic_config": false}

	mux := http.NewServeMux()
	h := func(pattern string, f func(w http.ResponseWriter, r *http.Request)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if *password != "" {
				if u, p, ok := r.BasicAuth(); !ok || u != "admin" || p != *password {
					w.WriteHeader(401)
					return
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if time.Now().Before(downTil) {
				log.Printf("%s %s -> (rebooting, connection dropped)", r.Method, r.URL.Path)
				if hj, ok := w.(http.Hijacker); ok {
					if c, _, err := hj.Hijack(); err == nil {
						c.Close()
						return
					}
				}
				bad(w, 503, "rebooting")
				return
			}
			log.Printf("%s %s", r.Method, r.URL.Path)
			f(w, r)
		})
	}

	h("GET /api/deviceinfo", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, info) })
	h("PUT /api/deviceinfo", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		for _, k := range []string{"long_name", "short_name", "ID", "colors"} {
			if v, ok := b[k]; ok {
				info[k] = v
			}
		}
		log.Printf("  name now %q / %q", info["long_name"], info["short_name"])
		w.WriteHeader(204)
	})
	h("GET /api/system/uptime", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, time.Since(started).Seconds()) })
	h("GET /api/ipsettings", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, ipset) })
	h("PUT /api/ipsettings", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		log.Printf("  IP change requested: %v (not applied by the fake)", b)
		w.WriteHeader(202)
	})
	h("GET /api/profile", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, []obj{{"id": 0, "name": "Show A", "exists": true}, {"id": 1, "name": "", "exists": false}, {"id": 2, "name": "Rig check", "exists": true}})
	})
	h("POST /api/profile/{idx}/recall", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("  recall profile %s %v", r.PathValue("idx"), body(r))
		w.WriteHeader(204)
	})
	h("POST /api/identify", func(w http.ResponseWriter, r *http.Request) { log.Printf("  *** IDENTIFY ***"); w.WriteHeader(204) })
	h("POST /api/reboot", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("  *** REBOOT *** (dropping connections for 8 s)")
		downTil = time.Now().Add(8 * time.Second)
		started = time.Now()
		w.WriteHeader(204)
	})
	h("POST /api/reset", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("  *** FACTORY RESET *** %v", body(r))
		w.WriteHeader(204)
	})
	h("GET /api/dmx/config", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, dmxCfg) })
	h("PUT /api/dmx/config", func(w http.ResponseWriter, r *http.Request) {
		for k, v := range body(r) {
			dmxCfg[k] = v
		}
		w.WriteHeader(204)
	})
	h("GET /api/dmx/ports", func(w http.ResponseWriter, r *http.Request) {
		frames += 40
		out := []obj{}
		for p := 0; p < nPorts; p++ {
			state := "idle"
			outFrames := 0
			for _, io := range ios {
				if io["io_class"] == "dmx" && int(toF(io["port_number"])) == p && io["io_type"] == "output" {
					state, outFrames = "streaming", frames
				}
			}
			out = append(out, obj{"backup_active_dmx_tx": false, "broken": p == 3, "stream_activity_state": state,
				"counters": obj{"input_frames": 0, "output_frames": outFrames, "pipeline_frames": outFrames}})
		}
		writeJSON(w, 200, out)
	})

	// IOs
	h("GET /api/IO", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, ios) })
	h("POST /api/IO", func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		class, _ := b["io_class"].(string)
		if class == "dmx" || class == "internal" || class == "" {
			bad(w, 400, "DMX and internal IOs can't be created")
			return
		}
		b["id"] = nextIO
		nextIO++
		defaults := obj{"source_ip": "0.0.0.0", "group": 0, "take_own_ip": true, "duplicate_ios": []any{}}
		for k, v := range defaults {
			if _, ok := b[k]; !ok {
				b[k] = v
			}
		}
		if class == "sacn" {
			if _, ok := b["per_channel_priority"]; !ok {
				b["per_channel_priority"] = false
			}
		}
		ios = append(ios, b)
		spreadNewControl(b)
		writeJSON(w, 200, obj{"index": b["id"]})
	})
	h("GET /api/IO/{idx}", func(w http.ResponseWriter, r *http.Request) {
		if io := findIO(idx(r.PathValue("idx"))); io != nil {
			writeJSON(w, 200, io)
			return
		}
		bad(w, 404, "no such IO")
	})
	h("PUT /api/IO/{idx}", func(w http.ResponseWriter, r *http.Request) {
		io := findIO(idx(r.PathValue("idx")))
		if io == nil {
			bad(w, 404, "no such IO")
			return
		}
		was := io["rdm"] == true
		for k, v := range body(r) {
			if k != "id" {
				io[k] = v
			}
		}
		afterIOPut(io, was)
		w.WriteHeader(204)
	})
	h("DELETE /api/IO/{idx}", func(w http.ResponseWriter, r *http.Request) {
		id := idx(r.PathValue("idx"))
		for i, io := range ios {
			if int(toF(io["id"])) == id {
				if io["io_class"] == "dmx" {
					bad(w, 400, "DMX IOs can't be deleted")
					return
				}
				ios = append(ios[:i], ios[i+1:]...)
				w.WriteHeader(204)
				return
			}
		}
		bad(w, 404, "no such IO")
	})

	// Process blocks
	h("GET /api/processblock", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, pbs) })
	h("GET /api/processblock/{idx}", func(w http.ResponseWriter, r *http.Request) {
		if p := pb(w, r.PathValue("idx")); p != nil {
			writeJSON(w, 200, p)
		}
	})
	h("PUT /api/processblock/{idx}", func(w http.ResponseWriter, r *http.Request) {
		if p := pb(w, r.PathValue("idx")); p != nil {
			updateBlock(idx(r.PathValue("idx")), body(r))
			w.WriteHeader(202)
		}
	})
	h("PATCH /api/processblock/{idx}", func(w http.ResponseWriter, r *http.Request) {
		if p := pb(w, r.PathValue("idx")); p != nil {
			b := body(r)
			if msg := patchRefusal(b); msg != "" {
				bad(w, 422, msg)
				return
			}
			updateBlock(idx(r.PathValue("idx")), b)
			w.WriteHeader(202)
		}
	})
	h("DELETE /api/processblock/{idx}", func(w http.ResponseWriter, r *http.Request) {
		i := idx(r.PathValue("idx"))
		if i < 0 || i >= len(pbs) {
			bad(w, 404, "no such processblock")
			return
		}
		if len(pbs[i]["inputs"].(obj)) > 0 || len(pbs[i]["outputs"].(obj)) > 0 {
			log.Printf("  (clearing PB%d while IOs still assigned — a real device may refuse this)", i)
		}
		pbs[i] = defaultPB(i)
		w.WriteHeader(202)
	})
	assignInput := func(w http.ResponseWriter, r *http.Request) {
		p := pb(w, r.PathValue("idx"))
		if p == nil {
			return
		}
		b := body(r)
		if findIO(int(toF(b["io_id"]))) == nil {
			bad(w, 400, "io_id does not exist")
			return
		}
		if n, err := strconv.Atoi(r.PathValue("slot")); err != nil || n > 3 {
			bad(w, 500, "Internal server error")
			return
		}
		if ioAttached(int(toF(b["io_id"]))) {
			bad(w, 500, "IO is already in use")
			return
		}
		p["inputs"].(obj)[r.PathValue("slot")] = int(toF(b["io_id"]))
		// Accepted whatever the mode, then what the mode does not take is dropped.
		if n, _ := strconv.Atoi(r.PathValue("slot")); n >= modeSlotsOr(p["mode"]) {
			delete(p["inputs"].(obj), r.PathValue("slot"))
		}
		w.WriteHeader(204)
	}
	h("PUT /api/processblock/{idx}/input/{slot}", assignInput)
	h("POST /api/processblock/{idx}/input/{slot}", assignInput)
	h("DELETE /api/processblock/{idx}/input/{slot}", func(w http.ResponseWriter, r *http.Request) {
		p := pb(w, r.PathValue("idx"))
		if p == nil {
			return
		}
		in := p["inputs"].(obj)
		if _, ok := in[r.PathValue("slot")]; !ok {
			bad(w, 404, "slot empty")
			return
		}
		delete(in, r.PathValue("slot"))
		w.WriteHeader(204)
	})
	// A control needs an IO of its own type, as on a real node: a plain input gets a 500.
	for ctrl, field := range ctrlFields {
		ctrl, field := ctrl, field
		h("PUT /api/processblock/{idx}/"+ctrl, func(w http.ResponseWriter, r *http.Request) {
			p := pb(w, r.PathValue("idx"))
			if p == nil {
				return
			}
			id := int(toF(body(r)["io_id"]))
			io := findIO(id)
			if io == nil || io["io_type"] != ctrl { // several blocks may share a control IO
				bad(w, 500, "Internal server error")
				return
			}
			p[field] = id
			w.WriteHeader(204)
		})
		h("DELETE /api/processblock/{idx}/"+ctrl, func(w http.ResponseWriter, r *http.Request) {
			if p := pb(w, r.PathValue("idx")); p != nil {
				p[field] = -1
				w.WriteHeader(204)
			}
		})
	}
	assignOutput := func(w http.ResponseWriter, r *http.Request) {
		p := pb(w, r.PathValue("idx"))
		if p == nil {
			return
		}
		b := body(r)
		id := int(toF(b["io_id"]))
		if findIO(id) == nil {
			bad(w, 400, "io_id does not exist")
			return
		}
		if ioAttached(id) {
			bad(w, 500, "IO is already in use")
			return
		}
		p["outputs"].(obj)[strconv.Itoa(id)] = id
		w.WriteHeader(204)
	}
	h("PUT /api/processblock/{idx}/output", assignOutput)
	h("POST /api/processblock/{idx}/output", assignOutput)
	h("DELETE /api/processblock/{idx}/output/{io}", func(w http.ResponseWriter, r *http.Request) {
		p := pb(w, r.PathValue("idx"))
		if p == nil {
			return
		}
		out := p["outputs"].(obj)
		if _, ok := out[r.PathValue("io")]; !ok {
			bad(w, 404, "output not assigned")
			return
		}
		delete(out, r.PathValue("io"))
		w.WriteHeader(204)
	})

	// Playlists / snapshots (startup buffer source)
	h("GET /api/play/playlist/{idx}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, obj{"name": "Playlist " + r.PathValue("idx"), "snapshots": []obj{
			{"id": "snap-full", "empty": false, "fade_in_time": 0, "hold_time": -1},
			{"id": "snap-empty", "empty": true, "fade_in_time": 0, "hold_time": -1},
		}})
	})
	h("GET /api/play/playlist/{idx}/snapshot/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "snap-empty" {
			writeJSON(w, 200, obj{"fade_in_time": 0, "hold_time": -1})
			return
		}
		ch := make([]int, 512)
		for i := range ch {
			ch[i] = (i * 7) % 256
		}
		writeJSON(w, 200, obj{"channels": ch, "fade_in_time": 0, "hold_time": -1})
	})
	rdmRoutes(h)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s -> 404", r.Method, r.URL.Path)
		bad(w, 404, "not found")
	})

	addr := fmt.Sprintf("%s:%d", *ip, *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	log.Printf("fake LumiNode %q serial %s at http://%s/api/deviceinfo", *name, *serial, addr)
	startArtnet(*ip, *artnetPort)

	var reg *exec.Cmd
	if !*noMdns {
		txt := []string{"product_type=" + info["type"].(string), "mac=" + info["mac_address"].(string), "firmware_version=v2.9.1",
			"serial=" + *serial, "device_name=" + *name, "ip_address=" + *ip + "/16"}
		reg = exec.Command("dns-sd", append([]string{"-R", *serial, "_luminex._tcp", "local", strconv.Itoa(*port)}, txt...)...)
		reg.Stdout, reg.Stderr = os.Stdout, os.Stderr
		if err := reg.Start(); err != nil {
			log.Printf("dns-sd not started (%v) — discovery won't find the fake; connect by IP", err)
			reg = nil
		} else {
			log.Printf("advertising %s via dns-sd (pid %d)", *serial, reg.Process.Pid)
		}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		if reg != nil {
			_ = reg.Process.Kill()
		}
		os.Exit(0)
	}()
	if err := http.Serve(ln, mux); err != nil && !strings.Contains(err.Error(), "closed") {
		log.Fatal(err)
	}
}
