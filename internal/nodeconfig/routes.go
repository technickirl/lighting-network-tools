package nodeconfig

// HTTP API the page talks to. Paths, bodies and response shapes are the v28
// Node contract. Every device error comes back as 502 {ok:false, error}.

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// validHost accepts only a dotted IPv4 address: every device this page talks to is reached by one. A host name
// would let a link (#node=<name>) or a request point the tool, and its password dialog, at any server by name.
func validHost(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil && strings.Count(s, ".") == 3 && !strings.Contains(s, ":")
}

var digits = regexp.MustCompile(`^\d{1,6}$`)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ok(w http.ResponseWriter, extra obj) {
	out := obj{"ok": true}
	for k, v := range extra {
		out[k] = v
	}
	writeJSON(w, 200, out)
}

func fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, obj{"ok": false, "error": err.Error()})
}

func deviceFail(w http.ResponseWriter, what string, err error) {
	luminex.Errorf("%s: %v", what, err)
	if errors.Is(err, errNeedsPassword) {
		writeJSON(w, http.StatusUnauthorized, obj{"ok": false, "needsPassword": true, "error": err.Error()})
		return
	}
	fail(w, 502, err)
}

func readBody(r *http.Request) json.RawMessage {
	data, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if len(data) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(data)
}

func decodeBody(r *http.Request, v any) error {
	return json.Unmarshal(readBody(r), v)
}

// ipParam validates {ip}; writes 400 and returns "" when bad.
func ipParam(w http.ResponseWriter, r *http.Request) string {
	ip := r.PathValue("ip")
	if !validHost(ip) {
		fail(w, 400, fmt.Errorf("bad device address %q", ip))
		return ""
	}
	return ip
}

// ── Snapshot ────────────────────────────────────────────────────────────────

// fetchSnapshot GETs everything the Overview needs, concurrently. Each part
// is optional except deviceinfo: when that fails the device is unreachable.
func fetchSnapshot(ip string) (obj, error) {
	luminex.Logf("fetching snapshot for %s", ip)
	paths := []string{"/api/deviceinfo", "/api/processblock", "/api/IO", "/api/dmx/ports", "/api/ipsettings", "/api/profile", "/api/system/uptime"}
	results := make([]json.RawMessage, len(paths))
	errs := make([]error, len(paths))
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			results[i], errs[i] = lumiGet(ip, p)
		}(i, p)
	}
	wg.Wait()
	for i, p := range paths {
		if errs[i] != nil {
			luminex.Errorf("  ✗ %s: %v", p, errs[i])
		}
	}
	snap := obj{"dryRun": luminex.DryRun}
	var uptime *float64
	if errs[6] == nil && results[6] != nil {
		var u float64
		if json.Unmarshal(results[6], &u) == nil {
			uptime = &u
		}
	}
	if errs[0] == nil {
		var d obj
		if json.Unmarshal(results[0], &d) == nil {
			snap["device"] = normaliseDevice(d, uptime)
		}
	}
	// Blocks and IOs resolve each other (an input slot shows "sACN U1", an IO
	// shows "used by PB 0"), so they are shaped together; a part that failed
	// is nil and the other side falls back to raw ids.
	var pbList, ioList []obj
	gotPBs := errs[1] == nil && json.Unmarshal(results[1], &pbList) == nil
	gotIOs := errs[2] == nil && json.Unmarshal(results[2], &ioList) == nil
	if gotPBs {
		snap["processBlocks"] = normaliseProcessBlocks(pbList, ioList) // ioList nil when the IO read failed
	}
	if gotIOs {
		addIOs(snap, ioList, pbList)
	}
	if errs[3] == nil {
		var list []obj
		if json.Unmarshal(results[3], &list) == nil {
			snap["dmxPorts"] = normaliseDmxPorts(list)
		}
	}
	if errs[4] == nil {
		var d obj
		if json.Unmarshal(results[4], &d) == nil {
			snap["network"] = normaliseNetwork(d)
		}
	}
	if errs[5] == nil {
		var list []obj
		if json.Unmarshal(results[5], &list) == nil {
			snap["profiles"] = normaliseProfiles(list)
		}
	}
	if errs[0] != nil {
		return snap, fmt.Errorf("Device unreachable: %w", errs[0])
	}
	luminex.Logf("snapshot complete for %s", ip)
	return snap, nil
}

func addIOs(into obj, list []obj, blocks []obj) {
	all := normaliseIOs(list, blocks)
	byClass := func(class string) []IO {
		out := []IO{}
		for _, io := range all {
			if io.IoClass == class {
				out = append(out, io)
			}
		}
		return out
	}
	into["dmxIOs"] = byClass("dmx")
	into["sacnIOs"] = byClass("sacn")
	into["allIOs"] = all
}

// addState shapes blocks and IOs together into the snapshot fields the page
// reads: processBlocks, dmxIOs, sacnIOs, allIOs.
func addState(into obj, pbList, ioList []obj) {
	into["processBlocks"] = normaliseProcessBlocks(pbList, ioList)
	addIOs(into, ioList, pbList)
}

// fetchState GETs the process blocks and the IO list concurrently.
func fetchState(ip string) (pbs, ios []obj, err error) {
	var e1, e2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pbs, e1 = lumiObjects(ip, "/api/processblock") }()
	go func() { defer wg.Done(); ios, e2 = lumiObjects(ip, "/api/IO") }()
	wg.Wait()
	if e1 != nil {
		return nil, nil, e1
	}
	return pbs, ios, e2
}

// fetchLiveUpdate gets the data that changes under an operator's hands: the
// blocks (active input, sources), the IOs and the DMX port state. Every call
// must succeed.
func fetchLiveUpdate(ip string) (obj, error) {
	var pbs, ios, ports []obj
	var e1, e2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pbs, ios, e1 = fetchState(ip) }()
	go func() { defer wg.Done(); ports, e2 = lumiObjects(ip, "/api/dmx/ports") }()
	wg.Wait()
	if e1 != nil {
		return nil, e1
	}
	if e2 != nil {
		return nil, e2
	}
	out := obj{}
	addState(out, pbs, ios)
	out["dmxPorts"] = normaliseDmxPorts(ports)
	return out, nil
}

// refreshedState re-reads blocks and IOs after a write, so the response
// carries both resolved views (a block edit changes what its IOs are used
// by, an IO edit changes what the blocks show).
func refreshedState(ip string) (obj, error) {
	pbs, ios, err := fetchState(ip)
	if err != nil {
		return nil, err
	}
	out := obj{}
	addState(out, pbs, ios)
	return out, nil
}

// ── Routes ──────────────────────────────────────────────────────────────────

// What a real LumiNode 12 (v2.9.1) does with a process block write, tested on
// 4 Nov 2026:
//   - PATCH replaces the block and needs inputs and outputs (422 without them).
//   - PUT is not a partial update: with a mode, every field it leaves out goes
//     back to its default (a mode alone blanks the name). Without a mode, a PUT
//     to a NONE block changed nothing. Inputs and outputs are kept.
//   - PUT with mode NONE clears the block: inputs and outputs go, the name
//     resets, and an IO left unused by that is deleted.
//
// So an edit reads the block and sends back all of its config with the edits on
// top. merge_map (CUSTOM mode) is not in what GET returns, so it cannot be kept.
var (
	blockConfigFields = []string{"name", "mode", "timeout", "backup_auto_recover", "ratelimiting", "colors", "masterlimit", "startup_buffer", "multi_channel_definitions"}
	blockEditFields   = []string{"name", "mode", "timeout", "backup_auto_recover", "ratelimiting", "masterlimit"}
)

// settableBlock keeps only the fields a PUT /api/processblock/<n> accepts. A
// block read back from the node also carries id, inputs, outputs, sources,
// status, patches and the control fields, which the node does not take on a
// write, so every PUT of a whole block goes through here.
func settableBlock(cur obj) obj {
	block := obj{}
	for _, k := range blockConfigFields {
		if v, ok := cur[k]; ok {
			block[k] = v
		}
	}
	return block
}

func editBlock(ip, idx string, edits obj) error {
	path := "/api/processblock/" + idx
	cur, err := lumiObject(ip, path)
	if err != nil {
		return err
	}
	block := settableBlock(cur)
	for _, k := range blockEditFields {
		if v, ok := edits[k]; ok {
			block[k] = v
		}
	}
	_, err = lumiDo("PUT", ip, path, block)
	return err
}

// nodeTurn gives each node one config write at a time. An edit is several calls (read the block, change it, put it
// back whole), so two pages working on one node, a laptop and an iPad say, could otherwise undo each other.
var nodeTurn sync.Map // ip → *sync.Mutex

func turnFor(ip string) *sync.Mutex {
	m, _ := nodeTurn.LoadOrStore(ip, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// guardWrites refuses a write to a device a job is working on, and makes config writes to one node take turns.
// Reads still go out, and so does the password route, which only reads the device. RDM requests are left out of
// the turns: they have their own line lock, and an ID OFF must not wait behind a config edit.
// Every route also has its {idx} and {pb} checked here: they go into the device's URL, so they must be numbers.
func guardWrites(pattern string, h http.HandlerFunc) http.HandlerFunc {
	write := strings.Contains(pattern, "/api/node/{ip}/") && !strings.HasPrefix(pattern, "GET ") &&
		!strings.HasSuffix(pattern, "/cache") && !strings.HasSuffix(pattern, "/password")
	takeTurn := write && !strings.Contains(pattern, "/rdm/")
	return func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"idx", "pb"} {
			if v := r.PathValue(name); v != "" && !digits.MatchString(v) {
				fail(w, 400, fmt.Errorf("bad number %q", v))
				return
			}
		}
		if !write {
			h(w, r)
			return
		}
		ip := r.PathValue("ip")
		if isLocked, why := locked(ip); isLocked {
			writeJSON(w, http.StatusConflict, obj{"ok": false, "locked": true, "error": why})
			return
		}
		if takeTurn && validHost(ip) {
			turn := turnFor(ip)
			turn.Lock()
			defer turn.Unlock()
			// A job may have started on this node while the request waited.
			if isLocked, why := locked(ip); isLocked {
				writeJSON(w, http.StatusConflict, obj{"ok": false, "locked": true, "error": why})
				return
			}
		}
		h(w, r)
	}
}

// handlePassword checks credentials against the device and keeps them if it accepts.
func handlePassword(w http.ResponseWriter, r *http.Request) {
	ip := ipParam(w, r)
	if ip == "" {
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeBody(r, &body); err != nil {
		fail(w, 400, fmt.Errorf("bad password request: %v", err))
		return
	}
	if body.Username == "" {
		body.Username = "admin"
	}
	auth := &luminex.Auth{Username: body.Username, Password: body.Password}
	res, err := luminex.Fetch(luminex.Call{Method: "GET", URL: lumiURL(ip, "/api/deviceinfo"), Accept: "application/json", Auth: auth, Timeout: deviceTimeout, QuietReply: true})
	if err != nil {
		deviceFail(w, "password", err)
		return
	}
	if luminex.Denied(res.Status) {
		deviceFail(w, "password", errNeedsPassword)
		return
	}
	luminex.RememberAuth(ip, auth)
	ok(w, nil)
}

func registerRoutes(mux *http.ServeMux) {
	handle := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, guardWrites(pattern, h)) }
	handle("GET /api/node/{ip}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		snap, err := fetchSnapshot(ip)
		if err != nil {
			deviceFail(w, "snapshot "+ip, err)
			return
		}
		snap["ip"] = ip
		ok(w, snap)
	})

	handle("PATCH /api/node/{ip}/processblock/{idx}", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		var edits obj
		if err := decodeBody(r, &edits); err != nil {
			fail(w, 400, fmt.Errorf("bad block edit: %v", err))
			return
		}
		if err := editBlock(ip, r.PathValue("idx"), edits); err != nil {
			deviceFail(w, "PB edit", err)
			return
		}
		pbs, err := refreshedState(ip)
		if err != nil {
			deviceFail(w, "PB edit", err)
			return
		}
		ok(w, pbs)
	})

	handle("DELETE /api/node/{ip}/processblock/{idx}", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		if _, err := lumiDo("DELETE", ip, "/api/processblock/"+r.PathValue("idx"), nil); err != nil {
			deviceFail(w, "PB delete", err)
			return
		}
		pbs, err := refreshedState(ip)
		if err != nil {
			deviceFail(w, "PB delete", err)
			return
		}
		ok(w, pbs)
	})

	handle("GET /api/node/{ip}/processblock/{idx}/raw", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		pb, err := lumiObject(ip, "/api/processblock/"+r.PathValue("idx"))
		if err != nil {
			deviceFail(w, "PB raw", err)
			return
		}
		n := 0
		if sb, ok := pb["startup_buffer"].([]any); ok {
			n = len(sb)
		}
		ok(w, obj{"startup_buffer_length": n})
	})

	handle("POST /api/node/{ip}/processblock/{idx}/wiring", handleWiring)
	handle("POST /api/node/{ip}/quicksetup", handleQuickSetup)

	handle("POST /api/node/{ip}/io", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		raw, err := lumiDo("POST", ip, "/api/IO", readBody(r))
		if err != nil {
			deviceFail(w, "IO create", err)
			return
		}
		ios, err := refreshedState(ip)
		if err != nil {
			deviceFail(w, "IO create", err)
			return
		}
		ios["newId"] = createdIndex(raw)
		ok(w, ios)
	})

	handle("PUT /api/node/{ip}/io/{idx}", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		if _, err := lumiDo("PUT", ip, "/api/IO/"+r.PathValue("idx"), readBody(r)); err != nil {
			deviceFail(w, "IO update", err)
			return
		}
		ios, err := refreshedState(ip)
		if err != nil {
			deviceFail(w, "IO update", err)
			return
		}
		ok(w, ios)
	})

	handle("DELETE /api/node/{ip}/io/{idx}", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		if _, err := lumiDo("DELETE", ip, "/api/IO/"+r.PathValue("idx"), nil); err != nil {
			deviceFail(w, "IO delete", err)
			return
		}
		ios, err := refreshedState(ip)
		if err != nil {
			deviceFail(w, "IO delete", err)
			return
		}
		ok(w, ios)
	})

	handle("POST /api/node/{ip}/profile/{idx}/recall", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		var body struct {
			KeepIP *bool `json:"keep_ip_settings"`
		}
		_ = decodeBody(r, &body)
		keep := body.KeepIP == nil || *body.KeepIP
		if _, err := lumiDo("POST", ip, "/api/profile/"+r.PathValue("idx")+"/recall", obj{"keep_ip_settings": keep}); err != nil {
			deviceFail(w, "profile recall", err)
			return
		}
		ok(w, nil)
	})

	handle("GET /api/node/{ip}/dmxconfig", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		cfg, err := lumiObject(ip, "/api/dmx/config")
		if err != nil {
			deviceFail(w, "dmx config", err)
			return
		}
		ok(w, cfg)
	})

	handle("PUT /api/node/{ip}/dmxconfig", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		if _, err := lumiDo("PUT", ip, "/api/dmx/config", readBody(r)); err != nil {
			deviceFail(w, "dmx config", err)
			return
		}
		ok(w, nil)
	})

	handle("POST /api/node/{ip}/identify", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		if _, err := lumiDo("POST", ip, "/api/identify", obj{}); err != nil {
			deviceFail(w, "identify", err)
			return
		}
		ok(w, nil)
	})

	handle("POST /api/node/{ip}/reboot", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		if _, err := lumiDo("POST", ip, "/api/reboot", obj{}); err != nil {
			deviceFail(w, "reboot", err)
			return
		}
		ok(w, nil)
	})

	handle("PUT /api/node/{ip}/ipsettings", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		var body struct {
			IP, Netmask, Gateway string
		}
		var in obj
		_ = decodeBody(r, &in)
		body.IP, body.Netmask, body.Gateway = str(in, "ipaddress", ""), str(in, "netmask", ""), str(in, "gateway", "0.0.0.0")
		if body.IP == "" || body.Netmask == "" {
			fail(w, 400, fmt.Errorf("ipaddress and netmask required"))
			return
		}
		if _, err := lumiDo("PUT", ip, "/api/ipsettings", obj{"ipaddress": body.IP, "netmask": body.Netmask, "gateway": body.Gateway}); err != nil {
			deviceFail(w, "IP settings", err)
			return
		}
		ok(w, nil)
	})

	handle("PUT /api/node/{ip}/devicename", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		var in obj
		_ = decodeBody(r, &in)
		cur, err := lumiObject(ip, "/api/deviceinfo")
		if err != nil {
			deviceFail(w, "device name", err)
			return
		}
		colors, _ := cur["colors"].([]any)
		if colors == nil {
			colors = []any{}
		}
		body := obj{
			"ID":         numOr(cur, "ID", 0),
			"long_name":  str(in, "long_name", ""),
			"short_name": str(in, "short_name", ""),
			"colors":     colors,
		}
		if _, err := lumiDo("PUT", ip, "/api/deviceinfo", body); err != nil {
			deviceFail(w, "device name", err)
			return
		}
		ok(w, nil)
	})

	// Startup buffer
	handle("GET /api/node/{ip}/startup/{pb}/snapshots", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		pl, err := lumiObject(ip, "/api/play/playlist/"+r.PathValue("pb"))
		if err != nil {
			deviceFail(w, "playlist", err)
			return
		}
		ok(w, obj{"playlist": pl})
	})

	handle("POST /api/node/{ip}/startup/{pb}/apply", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		pb := r.PathValue("pb")
		var in obj
		_ = decodeBody(r, &in)
		snapID := str(in, "snapshotId", "")
		if snapID == "" {
			fail(w, 400, fmt.Errorf("snapshotId required"))
			return
		}
		snapshot, err := lumiObject(ip, "/api/play/playlist/"+pb+"/snapshot/"+snapID)
		if err != nil {
			deviceFail(w, "startup buffer", err)
			return
		}
		channels, _ := snapshot["channels"].([]any)
		if channels == nil {
			deviceFail(w, "startup buffer", fmt.Errorf("No channels data in snapshot"))
			return
		}
		cur, err := lumiObject(ip, "/api/processblock/"+pb)
		if err != nil {
			deviceFail(w, "startup buffer", err)
			return
		}
		block := settableBlock(cur)
		block["startup_buffer"] = channels
		if _, err := lumiDo("PUT", ip, "/api/processblock/"+pb, block); err != nil {
			deviceFail(w, "startup buffer", err)
			return
		}
		luminex.Logf("startup buffer set for PB%s: %d channels from snapshot %s", pb, len(channels), snapID)
		ok(w, obj{"channels": len(channels)})
	})

	handle("DELETE /api/node/{ip}/startup/{pb}", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		pb := r.PathValue("pb")
		cur, err := lumiObject(ip, "/api/processblock/"+pb)
		if err != nil {
			deviceFail(w, "startup buffer clear", err)
			return
		}
		block := settableBlock(cur)
		block["startup_buffer"] = []any{}
		if _, err := lumiDo("PUT", ip, "/api/processblock/"+pb, block); err != nil {
			deviceFail(w, "startup buffer clear", err)
			return
		}
		ok(w, nil)
	})

	// "Cache" routes: the page calls these when a node is removed. There is no
	// server-side cache any more; they stop the live poller for that node.
	handle("DELETE /api/node/{ip}/cache", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		stopPoller(ip)
		ok(w, nil)
	})
	handle("DELETE /api/cache", func(w http.ResponseWriter, r *http.Request) {
		stopAllPollers()
		ok(w, nil)
	})

	registerRDMRoutes(handle)
	handle("GET /api/discover", handleDiscover)
	handle("POST /api/node/{ip}/password", handlePassword)
}

// ── Set input: wire one sACN/Art-Net input into slot 0 of a process block ──

func universeRange(protocol string) (int, int) {
	if protocol == "artnet" {
		return 0, 32767
	}
	return 1, 63999
}

// findOrCreateInput returns the id of an input IO of the given protocol and
// universe, creating one when none exists.
func findOrCreateInput(ip, protocol string, universe int, ios *[]obj) (int, error) {
	return findOrCreateIO(ip, protocol, "input", universe, ios)
}

// findOrCreateIO returns the id of a network IO of the given protocol, direction and
// universe, creating one when none exists. A new one is sACN multicast or Art-Net
// broadcast when it is an output. The created IO is appended to *ios so later lookups
// in the same batch reuse it.
func findOrCreateIO(ip, protocol, ioType string, universe int, ios *[]obj) (int, error) {
	for _, io := range *ios {
		if str(io, "io_class", "") == protocol && str(io, "io_type", "") == ioType && numOr(io, "universe", -1) == universe {
			id := numOr(io, "id", -1)
			luminex.Logf("  reusing %s IO id=%d U%d", protocol, id, universe)
			return id, nil
		}
	}
	var body obj
	if protocol == "sacn" {
		body = obj{"io_class": "sacn", "io_type": ioType, "name": fmt.Sprintf("sACN U%d", universe), "universe": universe,
			"priority": 100, "backup": false, "follow": false, "interweaving": false, "adaptive_discovery": true}
	} else {
		body = obj{"io_class": "artnet", "io_type": ioType, "name": fmt.Sprintf("Art-Net U%d", universe), "universe": universe,
			"source_ip": "0.0.0.0", "take_own_ip": true}
	}
	raw, err := lumiDo("POST", ip, "/api/IO", body)
	if err != nil {
		return -1, err
	}
	id := createdIndex(raw)
	*ios = append(*ios, obj{"id": float64(id), "io_class": protocol, "io_type": ioType, "universe": float64(universe)})
	luminex.Logf("  created %s %s IO id=%d U%d", protocol, ioType, id, universe)
	return id, nil
}

// dmxOutput returns the id of a physical port's DMX IO (0-based port), first making it
// an output if it is not one.
func dmxOutput(ip string, port int, ios []obj) (int, error) {
	var dmxIO obj
	for _, io := range ios {
		if str(io, "io_class", "") == "dmx" && numOr(io, "port_number", -1) == port {
			dmxIO = io
			break
		}
	}
	if dmxIO == nil {
		return -1, fmt.Errorf("no DMX IO found for port %d", port)
	}
	dmxID := numOr(dmxIO, "id", -1)
	if str(dmxIO, "io_type", "") != "output" {
		upd := obj{}
		for k, v := range dmxIO {
			upd[k] = v
		}
		upd["io_type"] = "output"
		if _, err := lumiDo("PUT", ip, "/api/IO/"+strconv.Itoa(dmxID), upd); err != nil {
			return -1, err
		}
	}
	return dmxID, nil
}

// blockInput is one slot's change from the block's wiring dialog: an input on a universe,
// or Clear to empty the slot.
type blockInput struct {
	Slot     int    `json:"slot"`
	Clear    bool   `json:"clear"`
	Protocol string `json:"protocol"`
	Universe *int   `json:"universe"`
}

func (in blockInput) check() error {
	if in.Slot < 0 || in.Slot > 255 {
		return fmt.Errorf("Slot %d does not exist", in.Slot+1)
	}
	if in.Clear {
		return nil
	}
	if in.Protocol != "sacn" && in.Protocol != "artnet" {
		return fmt.Errorf("Slot %d: protocol must be sacn or artnet", in.Slot+1)
	}
	lo, hi := universeRange(in.Protocol)
	if in.Universe == nil || *in.Universe < lo || *in.Universe > hi {
		return fmt.Errorf("Slot %d: universe must be %d-%d for %s", in.Slot+1, lo, hi, in.Protocol)
	}
	return nil
}

// attachedIOs lists every IO some block is using, as an input, an output or a control.
func attachedIOs(blocks []obj) map[int]bool {
	used := map[int]bool{}
	for _, b := range blocks {
		for _, side := range []string{"inputs", "outputs"} {
			if m, ok := b[side].(map[string]any); ok {
				for _, v := range m {
					if id, ok := v.(float64); ok {
						used[int(id)] = true
					}
				}
			}
		}
		for _, ctrl := range []string{"backup_ctrl", "switch_ctrl", "xfade_ctrl", "masterlimit_ctrl"} {
			if id, ok := b[ctrl].(float64); ok && id >= 0 {
				used[int(id)] = true
			}
		}
	}
	return used
}

// modeSlots is how many inputs each mode really keeps, measured on a LumiNode 12
// (v2.9.1). The node answers 204 to any of slots 0-3 in any mode, then drops what the
// mode does not take, so its answer cannot be relied on and the limit is checked here.
var modeSlots = map[string]int{"REROUTE": 1, "BACKUP": 2, "XFADE": 2, "SWITCH": 4, "HTP": 4, "LTP": 4, "CUSTOM": 4}

// setBlockInput changes one input slot of a block. A slot already on that universe
// is left alone, so its IO keeps its name and priority. Otherwise the old input comes
// off first and the IO list is read afterwards, because a real node can delete an IO
// that nothing uses any more (seen clearing a block). A node refuses an IO another
// block has attached, so an unattached IO is used or a new one created.
func setBlockInput(ip, pbIdx string, in blockInput) error {
	path := "/api/processblock/" + pbIdx + "/input/" + strconv.Itoa(in.Slot)
	block, err := lumiObject(ip, "/api/processblock/"+pbIdx)
	if err != nil {
		return err
	}
	ios, err := lumiObjects(ip, "/api/IO")
	if err != nil {
		return err
	}
	cur, occupied := -1, false
	if m, ok := block["inputs"].(map[string]any); ok {
		if id, ok := m[strconv.Itoa(in.Slot)].(float64); ok {
			cur, occupied = int(id), true
		}
	}
	if in.Clear {
		if !occupied {
			return nil
		}
		_, err := lumiDo("DELETE", ip, path, nil)
		return err
	}
	if limit, known := modeSlots[str(block, "mode", "")]; known && in.Slot >= limit {
		return fmt.Errorf("mode %s takes %d input(s)", str(block, "mode", ""), limit)
	}
	if occupied {
		for _, io := range ios {
			if numOr(io, "id", -1) == cur && str(io, "io_class", "") == in.Protocol && numOr(io, "universe", -1) == *in.Universe {
				luminex.Logf("  PB%s slot %d already on %s U%d", pbIdx, in.Slot, in.Protocol, *in.Universe)
				return nil
			}
		}
		if _, err := lumiDo("DELETE", ip, path, nil); err != nil {
			return err
		}
		if ios, err = lumiObjects(ip, "/api/IO"); err != nil {
			return err
		}
	}
	blocks, err := lumiObjects(ip, "/api/processblock")
	if err != nil {
		return err
	}
	attached := attachedIOs(blocks)
	free := make([]obj, 0, len(ios))
	for _, io := range ios {
		if !attached[numOr(io, "id", -1)] {
			free = append(free, io)
		}
	}
	ioID, err := findOrCreateInput(ip, in.Protocol, *in.Universe, &free)
	if err != nil {
		return err
	}
	if _, err := lumiDo("PUT", ip, path, obj{"io_id": ioID}); err != nil {
		return err
	}
	luminex.Logf("  PB%s slot %d -> IO %d OK", pbIdx, in.Slot, ioID)
	return nil
}

// blockControl is a control input's change: an IO on a universe, or Clear to take it off.
type blockControl struct {
	Role     string `json:"role"` // backup | switch | xfade | masterlimit
	Clear    bool   `json:"clear"`
	Protocol string `json:"protocol"`
	Universe *int   `json:"universe"`
}

// ctrlTypes is each control role's name in the API, which is also the io_type its IO
// must be created with: a real node answers 500 to a control on a plain input IO.
var ctrlTypes = map[string]string{"backup": "backup-ctrl", "switch": "switch-ctrl", "xfade": "x-fade-ctrl", "masterlimit": "masterlimit-ctrl"}

func (c blockControl) check() error {
	if _, known := ctrlTypes[c.Role]; !known {
		return fmt.Errorf("Unknown control %q", c.Role)
	}
	if c.Clear {
		return nil
	}
	if c.Protocol != "sacn" && c.Protocol != "artnet" {
		return fmt.Errorf("Control %s: protocol must be sacn or artnet", c.Role)
	}
	lo, hi := universeRange(c.Protocol)
	if c.Universe == nil || *c.Universe < lo || *c.Universe > hi {
		return fmt.Errorf("Control %s: universe must be %d-%d for %s", c.Role, lo, hi, c.Protocol)
	}
	return nil
}

// setBlockControl changes one control input on one block. Already on that universe is
// left alone; otherwise the old one comes off and a control IO on that universe is used,
// even if other blocks share it, which a node allows. Only when there is none is one
// created, and then it has to be taken back off the other blocks: a real node attaches
// a new control IO to every block with that control unset (LumiNode 12, v2.9.1).
func setBlockControl(ip, pbIdx string, c blockControl) error {
	ctrlType, field := ctrlTypes[c.Role], c.Role+"_ctrl"
	path := "/api/processblock/" + pbIdx + "/" + ctrlType
	block, err := lumiObject(ip, "/api/processblock/"+pbIdx)
	if err != nil {
		return err
	}
	cur := numOr(block, field, -1)
	if c.Clear {
		if cur < 0 {
			return nil
		}
		_, err := lumiDo("DELETE", ip, path, nil)
		return err
	}
	ios, err := lumiObjects(ip, "/api/IO")
	if err != nil {
		return err
	}
	ioID := -1
	for _, io := range ios {
		if str(io, "io_type", "") == ctrlType && str(io, "io_class", "") == c.Protocol && numOr(io, "universe", -1) == *c.Universe {
			if id := numOr(io, "id", -1); id == cur {
				return nil
			} else if ioID < 0 {
				ioID = id
			}
		}
	}
	if cur >= 0 {
		if _, err := lumiDo("DELETE", ip, path, nil); err != nil {
			return err
		}
	}
	if ioID >= 0 {
		_, err := lumiDo("PUT", ip, path, obj{"io_id": ioID})
		return err
	}

	before, err := lumiObjects(ip, "/api/processblock")
	if err != nil {
		return err
	}
	if ioID, err = findOrCreateIO(ip, c.Protocol, ctrlType, *c.Universe, &[]obj{}); err != nil {
		return err
	}
	if luminex.DryRun {
		_, err := lumiDo("PUT", ip, path, obj{"io_id": ioID})
		return err // nothing was created
	}
	// The IO now exists and the node has attached it to every block with this control unset, so it is taken
	// back off the others whether or not the assign below works.
	var putErr error
	if ioID < 0 {
		putErr = fmt.Errorf("the node made the control IO but did not say which one it is: check this block's %s control", c.Role)
	} else {
		_, putErr = lumiDo("PUT", ip, path, obj{"io_id": ioID})
	}
	detErr := detachNewControl(ip, pbIdx, ctrlType, field, ioID, before)
	switch {
	case putErr != nil && detErr != nil:
		return fmt.Errorf("%v; and %v", putErr, detErr)
	case putErr != nil:
		return putErr
	}
	return detErr
}

// detachNewControl takes a just-created control IO off every block that had that control
// unset before it was created, apart from the one it was made for. With ioID unknown (-1)
// it takes off whatever such a block has gained.
func detachNewControl(ip, pbIdx, ctrlType, field string, ioID int, before []obj) error {
	wasUnset := map[int]bool{}
	for _, b := range before {
		if numOr(b, field, -1) < 0 {
			wasUnset[numOr(b, "id", -1)] = true
		}
	}
	after, err := lumiObjects(ip, "/api/processblock")
	if err != nil {
		return fmt.Errorf("could not check other blocks for the new control: %v", err)
	}
	var stuck []string
	for _, b := range after {
		id := numOr(b, "id", -1)
		got := numOr(b, field, -1)
		if strconv.Itoa(id) == pbIdx || !wasUnset[id] || got < 0 || (ioID >= 0 && got != ioID) {
			continue
		}
		if _, err := lumiDo("DELETE", ip, fmt.Sprintf("/api/processblock/%d/%s", id, ctrlType), nil); err != nil {
			stuck = append(stuck, fmt.Sprintf("PB %d", id))
		}
	}
	if len(stuck) > 0 {
		return fmt.Errorf("the node also attached it to %s, and it could not be taken off", strings.Join(stuck, ", "))
	}
	return nil
}

// blockOutput is an output to add: a network universe, or a DMX port (1-based).
type blockOutput struct {
	Protocol string `json:"protocol"` // sacn | artnet | dmx
	Universe *int   `json:"universe"`
	Port     *int   `json:"port"`
}

func (o blockOutput) String() string {
	switch {
	case o.Protocol == "dmx" && o.Port != nil:
		return fmt.Sprintf("DMX Port %d", *o.Port)
	case o.Universe != nil:
		return fmt.Sprintf("%s U%d", upper(o.Protocol), *o.Universe)
	}
	return o.Protocol
}

func (o blockOutput) check() error {
	if o.Protocol == "dmx" {
		if o.Port == nil || *o.Port < 1 || *o.Port > 256 {
			return fmt.Errorf("Output: DMX port must be a port number")
		}
		return nil
	}
	if o.Protocol != "sacn" && o.Protocol != "artnet" {
		return fmt.Errorf("Output: type must be sacn, artnet or dmx")
	}
	lo, hi := universeRange(o.Protocol)
	if o.Universe == nil || *o.Universe < lo || *o.Universe > hi {
		return fmt.Errorf("Output: universe must be %d-%d for %s", lo, hi, o.Protocol)
	}
	return nil
}

// addBlockOutput attaches an output. One the block already has is left alone. A DMX
// port another block is driving is refused here with a clear reason; a network output
// uses an unattached IO or a new one.
func addBlockOutput(ip, pbIdx string, o blockOutput) error {
	ios, err := lumiObjects(ip, "/api/IO")
	if err != nil {
		return err
	}
	blocks, err := lumiObjects(ip, "/api/processblock")
	if err != nil {
		return err
	}
	var ioID int
	if o.Protocol == "dmx" {
		for _, b := range blocks {
			m, _ := b["outputs"].(map[string]any)
			for _, v := range m {
				id, _ := v.(float64)
				for _, io := range ios {
					if numOr(io, "id", -1) != int(id) || str(io, "io_class", "") != "dmx" || numOr(io, "port_number", -1) != *o.Port-1 {
						continue
					}
					if strconv.Itoa(numOr(b, "id", -1)) == pbIdx {
						return nil
					}
					return fmt.Errorf("DMX Port %d is already the output of PB %d", *o.Port, numOr(b, "id", -1))
				}
			}
		}
		if ioID, err = dmxOutput(ip, *o.Port-1, ios); err != nil {
			return err
		}
	} else {
		attached := attachedIOs(blocks)
		for _, b := range blocks {
			if strconv.Itoa(numOr(b, "id", -1)) != pbIdx {
				continue
			}
			m, _ := b["outputs"].(map[string]any)
			for _, v := range m {
				id, _ := v.(float64)
				for _, io := range ios {
					if numOr(io, "id", -1) == int(id) && str(io, "io_class", "") == o.Protocol && numOr(io, "universe", -1) == *o.Universe {
						return nil
					}
				}
			}
		}
		free := make([]obj, 0, len(ios))
		for _, io := range ios {
			if !attached[numOr(io, "id", -1)] {
				free = append(free, io)
			}
		}
		if ioID, err = findOrCreateIO(ip, o.Protocol, "output", *o.Universe, &free); err != nil {
			return err
		}
	}
	_, err = lumiDo("PUT", ip, "/api/processblock/"+pbIdx+"/output", obj{"io_id": ioID})
	return err
}

// handleWiring applies what the dialog changed on a block: input slots, outputs taken
// off, outputs added, then controls. Everything is checked before anything is sent. After that,
// each change is its own set of device calls, so one failing does not stop the others,
// and the response lists failures.
func handleWiring(w http.ResponseWriter, r *http.Request) {
	ip := ipParam(w, r)
	if ip == "" {
		return
	}
	pbIdx := r.PathValue("idx")
	var body struct {
		Inputs        []blockInput   `json:"inputs"`
		RemoveOutputs []int          `json:"removeOutputs"`
		AddOutputs    []blockOutput  `json:"addOutputs"`
		Controls      []blockControl `json:"controls"`
	}
	if err := decodeBody(r, &body); err != nil {
		fail(w, 400, fmt.Errorf("bad wiring: %v", err))
		return
	}
	for _, in := range body.Inputs {
		if err := in.check(); err != nil {
			fail(w, 400, err)
			return
		}
	}
	for _, o := range body.AddOutputs {
		if err := o.check(); err != nil {
			fail(w, 400, err)
			return
		}
	}
	for _, c := range body.Controls {
		if err := c.check(); err != nil {
			fail(w, 400, err)
			return
		}
	}
	errs := []string{}
	var denied error
	note := func(what string, err error) {
		if err == nil {
			return
		}
		if errors.Is(err, errNeedsPassword) {
			denied = err
			return
		}
		luminex.Errorf("wiring PB%s, %s: %v", pbIdx, what, err)
		errs = append(errs, fmt.Sprintf("%s: %v", what, err))
	}
	for _, in := range body.Inputs {
		note(fmt.Sprintf("Slot %d", in.Slot+1), setBlockInput(ip, pbIdx, in))
	}
	for _, id := range body.RemoveOutputs {
		_, err := lumiDo("DELETE", ip, fmt.Sprintf("/api/processblock/%s/output/%d", pbIdx, id), nil)
		note(fmt.Sprintf("Removing output %d", id), err)
	}
	for _, o := range body.AddOutputs {
		note("Output "+o.String(), addBlockOutput(ip, pbIdx, o))
	}
	for _, c := range body.Controls {
		note("Control "+c.Role, setBlockControl(ip, pbIdx, c))
	}
	if denied != nil {
		deviceFail(w, "wiring", denied)
		return
	}
	state, err := refreshedState(ip)
	if err != nil {
		deviceFail(w, "wiring", err)
		return
	}
	state["errors"] = errs
	ok(w, state)
}

// ── Quick Setup: one REROUTE process block per DMX port ─────────────────────
//
// Body: {ports:[{port: 1-based, universe}]}. The protocol per port is whatever
// the block's current input uses (default sACN). The device tracks IO usage
// globally, so a block's IO assignments are removed before the block is
// cleared and rebuilt; otherwise the device still thinks the IOs are in use.

func handleQuickSetup(w http.ResponseWriter, r *http.Request) {
	ip := ipParam(w, r)
	if ip == "" {
		return
	}
	var in struct {
		Ports []struct {
			Port     int `json:"port"`
			Universe int `json:"universe"`
		} `json:"ports"`
	}
	_ = decodeBody(r, &in)
	if len(in.Ports) == 0 {
		fail(w, 400, fmt.Errorf("No ports provided"))
		return
	}
	luminex.Logf("quick setup for %s: %v", ip, in.Ports)

	var ios, pbList []obj
	var e1, e2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); ios, e1 = lumiObjects(ip, "/api/IO") }()
	go func() { defer wg.Done(); pbList, e2 = lumiObjects(ip, "/api/processblock") }()
	wg.Wait()
	if e1 != nil || e2 != nil {
		err := e1
		if err == nil {
			err = e2
		}
		deviceFail(w, "quick setup", fmt.Errorf("Failed to fetch device state: %v", err))
		return
	}

	errs := []string{}
	held := attachedIOs(pbList) // kept up to date as each port is rebuilt
	for _, p := range in.Ports {
		pbIdx := p.Port - 1 // process blocks and physical ports are 0-based
		if err := quickSetupPort(ip, pbIdx, p.Port, p.Universe, &ios, pbList, held); err != nil {
			luminex.Errorf("  port %d: %v", p.Port, err)
			errs = append(errs, fmt.Sprintf("Port %d: %v", p.Port, err))
		}
	}

	snap, err := fetchSnapshot(ip)
	if err != nil {
		errs = append(errs, err.Error())
		writeJSON(w, 200, obj{"ok": false, "errors": errs})
		return
	}
	snap["ok"] = len(errs) == 0
	snap["errors"] = errs
	writeJSON(w, 200, snap)
}

func findPB(list []obj, id int) obj {
	for _, pb := range list {
		if numOr(pb, "id", -1) == id {
			return pb
		}
	}
	return nil
}

func findIO(list []obj, id int) obj {
	for _, io := range list {
		if numOr(io, "id", -1) == id {
			return io
		}
	}
	return nil
}

// held is every IO a block has attached. The node refuses an IO another block holds, and by then this port's block
// has been cleared, which would leave the port with no input and no output. So the input is an IO no other block
// holds (the block's own inputs count as free, as they are about to come off it), or a new one.
func quickSetupPort(ip string, pbIdx, port, universe int, ios *[]obj, pbList []obj, held map[int]bool) error {
	// Current protocol = class of the block's first input IO, else sACN.
	protocol := "sacn"
	pb := findPB(pbList, pbIdx)
	if pb != nil {
		if inputs := mapValues(pb["inputs"]); len(inputs) > 0 {
			if cur := findIO(*ios, inputs[0]); cur != nil {
				if c := str(cur, "io_class", ""); c == "artnet" || c == "sacn" {
					protocol = c
				}
			}
		}
	}
	luminex.Logf("  port %d: protocol=%s, new universe=%d", port, protocol, universe)
	lo, hi := universeRange(protocol)
	if universe < lo || universe > hi {
		return fmt.Errorf("universe %d out of range for %s (%d-%d)", universe, protocol, lo, hi)
	}

	dmxID, err := dmxOutput(ip, pbIdx, *ios)
	if err != nil {
		return err
	}
	luminex.Logf("  port %d: DMX IO id=%d", port, dmxID)

	own := map[int]bool{}
	if pb != nil {
		for _, id := range mapValues(pb["inputs"]) {
			own[id] = true
		}
	}
	free := make([]obj, 0, len(*ios))
	for _, io := range *ios {
		if id := numOr(io, "id", -1); own[id] || !held[id] {
			free = append(free, io)
		}
	}
	known := len(free)
	inputID, err := findOrCreateInput(ip, protocol, universe, &free)
	if err != nil {
		return err
	}
	if len(free) > known {
		*ios = append(*ios, free[known:]...) // a new IO: later ports can see it
	}

	// 1. Detach the block's current IOs (inputs keyed by slot, outputs by IO id).
	if pb != nil {
		for _, slot := range mapKeys(pb["inputs"]) {
			if _, err := lumiDo("DELETE", ip, fmt.Sprintf("/api/processblock/%d/input/%d", pbIdx, slot), nil); err == nil {
				luminex.Logf("  port %d: removed input slot %d", port, slot)
			}
		}
		for _, ioID := range mapKeys(pb["outputs"]) {
			if _, err := lumiDo("DELETE", ip, fmt.Sprintf("/api/processblock/%d/output/%d", pbIdx, ioID), nil); err == nil {
				luminex.Logf("  port %d: removed output IO %d", port, ioID)
			}
		}
	}
	// 2. Clear and rebuild the block.
	if _, err := lumiDo("DELETE", ip, fmt.Sprintf("/api/processblock/%d", pbIdx), nil); err != nil {
		return err
	}
	for id := range own {
		delete(held, id)
	}
	block := obj{
		"name": fmt.Sprintf("Port %d U%d", port, universe), "mode": "REROUTE",
		"ratelimiting": true, "timeout": 0, "backup_auto_recover": true, "masterlimit": "NONE",
		"colors": []string{"#00ffa1"}, "startup_buffer": []any{}, "multi_channel_definitions": []any{},
		"inputs": obj{}, "outputs": obj{},
	}
	if _, err := lumiDo("PUT", ip, fmt.Sprintf("/api/processblock/%d", pbIdx), block); err != nil {
		return err
	}
	// 3. Assign the new IOs.
	if _, err := lumiDo("PUT", ip, fmt.Sprintf("/api/processblock/%d/input/0", pbIdx), obj{"io_id": inputID}); err != nil {
		return fmt.Errorf("block cleared, but its input was refused, so the port has no input now: %w", err)
	}
	held[inputID] = true
	if _, err := lumiDo("PUT", ip, fmt.Sprintf("/api/processblock/%d/output", pbIdx), obj{"io_id": dmxID}); err != nil {
		return err
	}
	luminex.Logf("  port %d: PB%d REROUTE %s U%d -> DMX%d OK", port, pbIdx, upper(protocol), universe, pbIdx)
	return nil
}

// ── Discovery ───────────────────────────────────────────────────────────────

const discoverScan = 6 * time.Second

// handleDiscover browses _luminex._tcp, then confirms each address with
// GET /api/deviceinfo (3 s) so only LumiNode/LumiCore devices are listed.
func handleDiscover(w http.ResponseWriter, r *http.Request) {
	luminex.Logf("mDNS scan started (%s)", luminex.DiscoveryInfo().Backend)
	devs, err := luminex.Scan(discoverScan)
	if err != nil {
		// The page shows this as "Scan failed: …" rather than "No LumiNodes found".
		deviceFail(w, "discovery", err)
		return
	}
	type found struct {
		IP       string `json:"ip"`
		Port     int    `json:"port"`
		MdnsName string `json:"mdnsName"`
		Model    string `json:"model"`
		Serial   string `json:"serial"`
		Name     string `json:"name"`
	}
	results := make([]*found, len(devs))
	var wg sync.WaitGroup
	for i, d := range devs {
		if d.IP == "" {
			continue
		}
		wg.Add(1)
		go func(i int, d luminex.Device) {
			defer wg.Done()
			res, err := luminex.Fetch(luminex.Call{Method: "GET", URL: lumiURL(d.IP, "/api/deviceinfo"), Timeout: 3 * time.Second, QuietReply: true})
			if err != nil {
				luminex.Logf("  %s (%s): not a LumiNode: %v", d.Instance, d.IP, err)
				return
			}
			if res.Status != 200 {
				luminex.Logf("  %s (%s): /api/deviceinfo -> %d, skipped", d.Instance, d.IP, res.Status)
				return
			}
			var info obj
			if json.Unmarshal([]byte(res.Text), &info) != nil || (str(info, "type", "") == "" && str(info, "serial", "") == "") {
				return
			}
			name := str(info, "long_name", "")
			if name == "" {
				name = str(info, "short_name", d.Instance)
			}
			results[i] = &found{IP: d.IP, Port: 80, MdnsName: d.Instance, Model: str(info, "type", "LumiNode"), Serial: str(info, "serial", "—"), Name: name}
			luminex.Logf("  ✓ %s %q @ %s", results[i].Model, name, d.IP)
		}(i, d)
	}
	wg.Wait()
	out := []*found{}
	seenIP := map[string]bool{}
	for _, f := range results {
		if f != nil && !seenIP[f.IP] { // one card per address
			seenIP[f.IP] = true
			out = append(out, f)
		}
	}
	luminex.Logf("discovery done: %d LumiNode(s) confirmed of %d service(s)", len(out), len(devs))
	ok(w, obj{"devices": out, "scanned": len(devs)})
}
