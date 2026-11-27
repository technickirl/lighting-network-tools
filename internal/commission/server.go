package commission

// Commissioning: the scanned device list, the apply and reset jobs that run
// against it and the page that drives them. A tool mounts all this on its own
// server. There is one set of state per process.

import (
	"embed"
	"encoding/json"
	"fmt"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

// Options is what a tool tells the page about itself.
type Options struct {
	Version      string
	Title        string // shown instead of the page's default title; empty keeps it
	ConfigureURL string // where the node configure page is mounted; empty means this tool has none

	// For tests: stand-ins for the network scan and the machine's adapters.
	Scan         func(time.Duration) ([]luminex.Device, error)
	Reachability func(ip string, prefix int) luminex.Reach
}

var options Options

//go:embed public
var publicFS embed.FS

type State struct {
	mu         sync.Mutex
	devices    []luminex.Device
	scanning   bool
	lastScanAt int64
	scanError  string
	goodIfaces []luminex.Iface // this computer's addresses the last time a device was reachable
	job        *Job
	scanLock   sync.Mutex
	unticked   map[string]bool // device ids the operator unticked; kept across scans until the tool quits

	topology        *Topology // what each device is plugged into (topology.go)
	readingTopology bool
	settings        *SettingsRun      // the latest name / LED / identify run (settings.go)
	routes          map[string]string // device id -> IPv6 route, for devices sharing an address (shared.go)
}

var state = &State{unticked: map[string]bool{}}

// localIfaces and reachability are variables so tests can stand in for the machine.
var (
	localIfaces  = luminex.LocalInterfaces
	reachability = luminex.Reachability
)

type Snapshot struct {
	Version      string              `json:"version"`
	Title        string              `json:"title"`
	ConfigureURL string              `json:"configureUrl"`
	DryRun       bool                `json:"dryRun"`
	Discovery    luminex.BackendInfo `json:"discovery"`
	Interfaces   []luminex.Iface     `json:"interfaces"`
	Devices      []luminex.Device    `json:"devices"`
	Scanning     bool                `json:"scanning"`
	LastScanAt   int64               `json:"lastScanAt"`
	ScanError    string              `json:"scanError"`
	LinkLost     []luminex.Iface     `json:"linkLost"` // addresses this computer had when devices were last reachable, gone now
	Job          *JobSnapshot        `json:"job"`
	Unticked     []string            `json:"unticked"`
	Gen2Verified bool                `json:"gen2Verified"`
	Topology     *Topology           `json:"topology"`
	ReadingTopo  bool                `json:"readingTopology"`
	Settings     *SettingsSnapshot   `json:"settings"`
	OwnClashes   []OwnClash          `json:"ownClashes"` // devices on this computer's own address
}

func snapshot() []byte {
	ifaces := localIfaces()
	state.mu.Lock()
	s := Snapshot{Version: options.Version, Title: options.Title, ConfigureURL: options.ConfigureURL, DryRun: luminex.DryRun, Discovery: luminex.DiscoveryInfo(), Interfaces: ifaces,
		// A copy: scans and jobs rewrite entries of the list in place while this one is being sent to the pages.
		Devices: append([]luminex.Device(nil), state.devices...), Scanning: state.scanning, LastScanAt: state.lastScanAt, ScanError: state.scanError,
		LinkLost: luminex.LostInterfaces(state.goodIfaces, ifaces), Gen2Verified: kinds["gen2"].verified}
	job, settings := state.job, state.settings
	s.Topology, s.ReadingTopo = state.topology, state.readingTopology
	s.Unticked = make([]string, 0, len(state.unticked))
	for id := range state.unticked {
		s.Unticked = append(s.Unticked, id)
	}
	state.mu.Unlock()
	sort.Strings(s.Unticked)
	s.OwnClashes = []OwnClash{}
	if os.Getenv("DEVICE_PORT") == "" { // the test fakes answer on this computer's own address
		s.OwnClashes = ownAddressClashes(s.Devices, s.Topology, ifaces)
	}
	if s.LinkLost == nil {
		s.LinkLost = []luminex.Iface{}
	}
	if s.Devices == nil {
		s.Devices = []luminex.Device{}
	}
	if s.Interfaces == nil {
		s.Interfaces = []luminex.Iface{}
	}
	if job != nil {
		s.Job = job.snapshot()
	}
	if settings != nil {
		s.Settings = settings.snapshot()
	}
	b, _ := json.Marshal(s)
	return b
}

var (
	clientsMu sync.Mutex
	clients   = map[chan []byte]struct{}{}
	notifyCh  = make(chan struct{}, 1)
)

func notify() {
	select {
	case notifyCh <- struct{}{}:
	default:
	}
}

func broadcaster() {
	for range notifyCh {
		time.Sleep(80 * time.Millisecond)
		clientsMu.Lock()
		open := len(clients)
		clientsMu.Unlock()
		if open == 0 {
			continue // no page open: a page gets the state when it connects
		}
		data := snapshot()
		clientsMu.Lock()
		for ch := range clients {
			select {
			case ch <- data:
			default:
			}
		}
		clientsMu.Unlock()
	}
}

func enrich(d luminex.Device) luminex.Device {
	r := reachability(d.IP, d.Prefix)
	d.Reachable, d.ReachVia, d.ReachReason, d.ReachFix, d.ReachWarning = r.Reachable, r.Via, r.Reason, r.Fix, r.Warning
	d.APIKind = luminex.APIKind(d)
	return d
}

func doScan() {
	state.mu.Lock()
	if state.scanning {
		state.mu.Unlock()
		return
	}
	state.scanning, state.scanError = true, ""
	state.mu.Unlock()
	notify()
	state.scanLock.Lock()
	found, err := scanFor(5 * time.Second)
	state.scanLock.Unlock()
	if err != nil {
		// The scan did not run, so the previous list is still the best we
		// know; the UI shows the error as a banner above it.
		state.mu.Lock()
		state.scanError, state.scanning = err.Error(), false
		state.mu.Unlock()
		notify()
		return
	}
	devs := make([]luminex.Device, 0, len(found))
	state.mu.Lock()
	previous := append([]luminex.Device(nil), state.devices...)
	state.mu.Unlock()
	anyReachable := false
	for _, d := range found {
		e := enrich(d)
		anyReachable = anyReachable || e.Reachable
		devs = append(devs, e)
	}
	// A device added by address stays listed: mDNS may simply not be finding it.
	for _, p := range previous {
		if p.Manual && findSame(devs, p) == nil {
			devs = append(devs, p)
		}
	}
	routes := map[string]string{}
	if checkShared() {
		routes = findRoutes(devs)
	}
	markShared(devs, routes)
	state.mu.Lock()
	state.routes = routes
	state.devices, state.lastScanAt, state.scanning = devs, nowMs(), false
	if anyReachable {
		state.goodIfaces = localIfaces() // a known-good picture of this computer's network
	}
	job := state.job
	state.mu.Unlock()
	if job != nil && !job.snapshot().Done {
		job.onScan(found)
	}
	notify()
	readTopology() // after every scan, so the numbering knows the cabling
}

// watchNetwork keeps the page in step with this computer's own network. A new
// address (a network card plugged in, a cable connected, an address added to
// the adapter) starts a device scan, as devices on it may now be in reach. A
// lost one only refreshes the page: scanning then would empty the device list,
// and the link-down banner says what happened.
func watchNetwork() {
	luminex.WatchInterfaces(nil, localIfaces, 2*time.Second, func(added, removed []luminex.Iface) {
		if len(removed) > 0 {
			luminex.Logf("This computer lost %s", luminex.DescribeInterfaces(removed))
		}
		notify()
		if len(added) == 0 {
			return
		}
		luminex.Logf("This computer gained %s: scanning", luminex.DescribeInterfaces(added))
		go func() {
			time.Sleep(time.Second) // let the new connection settle before the mDNS query goes out on it
			for {
				state.mu.Lock()
				busy := state.scanning
				state.mu.Unlock()
				if !busy { // a scan already running began before the new address was there
					break
				}
				time.Sleep(300 * time.Millisecond)
			}
			doScan()
		}()
	})
}

// mergeSeen folds a job's verify-scan results into the device list, so a
// device that has just been re-addressed shows (and is addressed at) its new
// IP without a manual rescan. Devices the short scan did not see are kept.
func mergeSeen(found []luminex.Device) {
	if len(found) == 0 {
		return
	}
	state.mu.Lock()
	changed := false
	anyReachable := false
	for _, f := range found {
		d := enrich(f)
		anyReachable = anyReachable || d.Reachable
		replaced := false
		for i, cur := range state.devices {
			if !luminex.SameDevice(cur, d) {
				continue
			}
			if cur.IP != d.IP || cur.Prefix != d.Prefix || cur.Name != d.Name {
				luminex.Logf("[%s] device list updated: %s/%d %q → %s/%d %q", d.ID, cur.IP, cur.Prefix, cur.Name, d.IP, d.Prefix, d.Name)
				changed = true
			}
			state.devices[i] = d
			replaced = true
			break
		}
		if !replaced {
			state.devices = append(state.devices, d)
			changed = true
		}
	}
	if anyReachable {
		state.goodIfaces = localIfaces()
	}
	markShared(state.devices, state.routes) // enrich above dropped it, and a move may have ended the sharing
	state.mu.Unlock()
	if changed {
		notify()
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

func handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	ch := make(chan []byte, 4)
	clientsMu.Lock()
	clients[ch] = struct{}{}
	clientsMu.Unlock()
	defer func() {
		clientsMu.Lock()
		delete(clients, ch)
		clientsMu.Unlock()
	}()
	fmt.Fprintf(w, "event: state\ndata: %s\n\n", snapshot())
	fl.Flush()
	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-ch:
			fmt.Fprintf(w, "event: state\ndata: %s\n\n", data)
			fl.Flush()
		case <-hb.C:
			fmt.Fprint(w, ": hb\n\n")
			fl.Flush()
		}
	}
}

// dedupe keeps the first occurrence of each id, so a device is never written twice.
func dedupe(ids []string) []string {
	seen := map[string]bool{}
	out := ids[:0:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// lookup finds scanned devices by id; returns an error message if any is missing.
func lookup(ids []string) ([]luminex.Device, string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	var out []luminex.Device
	for _, id := range ids {
		found := false
		for _, d := range state.devices {
			if d.ID == id {
				if d.SharedIP && d.LinkRoute == "" {
					return nil, fmt.Sprintf("%s shares %s and does not answer over IPv6. Connect it on its own, then Scan.", d.Serial, d.IP)
				}
				out = append(out, d)
				found = true
				break
			}
		}
		if !found {
			return nil, "Selected devices have left the scan list. Scan again."
		}
	}
	return out, ""
}

func validIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

// badNewIP says why ip cannot be given to a device, or "". The page refuses these
// too; the server mirrors it, as the configure page's Network dialog and anything
// else that posts here must not be able to send one through.
func badNewIP(ip string) string {
	a := net.ParseIP(ip)
	if a == nil || a.To4() == nil {
		return "Invalid new IP " + ip
	}
	a = a.To4()
	switch {
	case a.IsUnspecified(), a[0] == 0:
		return ip + " is not an address a device can use."
	case a.IsLoopback():
		return ip + " is a loopback address."
	case a.IsMulticast(), a.Equal(net.IPv4bcast):
		return ip + " is a multicast or broadcast address."
	}
	if name := luminex.OwnAddress(ip, localIfaces()); name != "" {
		return ip + " is this computer's own address (" + name + ")."
	}
	return ""
}

// edgeOfSubnet refuses the network and broadcast addresses of the mask being set: no device can use them.
func edgeOfSubnet(ip, mask string) string {
	a, m := net.ParseIP(ip).To4(), net.IPMask(net.ParseIP(mask).To4())
	if ones, _ := m.Size(); a == nil || ones > 30 {
		return ""
	}
	allZero, allOne := true, true
	for i := range a {
		host := a[i] &^ m[i]
		allZero = allZero && host == 0
		allOne = allOne && host == ^m[i]
	}
	switch {
	case allZero:
		return ip + " is the network address for " + mask + "."
	case allOne:
		return ip + " is the broadcast address for " + mask + "."
	}
	return ""
}

// addressClash mirrors the page's checks, for any caller that skips them (the Configure page's Network dialog, a
// second device): two devices in one request getting the same address, or an address a listed device holds now.
// A holder that is being moved in this same request still counts, as the two changes are not ordered.
func addressClash(targets map[string]string) string {
	byIP := map[string]string{}
	for id, ip := range targets {
		if other, dup := byIP[ip]; dup {
			a, b := id, other
			if b < a {
				a, b = b, a
			}
			return fmt.Sprintf("%s and %s would both get %s.", a, b, ip)
		}
		byIP[ip] = id
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, d := range state.devices {
		if id, wanted := byIP[d.IP]; wanted && id != d.ID {
			name := d.Name
			if name == "" {
				name = d.Serial
			}
			return fmt.Sprintf("%s is in use by %s (%s). Move that device first, or tick \"Ignore address clashes\".", d.IP, name, d.Serial)
		}
	}
	return ""
}

// validMask accepts a dotted IPv4 netmask whose ones are contiguous and
// between /1 and /31. maskToCIDR turns anything else into 0, which would
// send prefix_length 0 to a switch, so the mask is checked here first.
func validMask(s string) bool {
	if !validIPv4(s) {
		return false
	}
	ones, bits := net.IPMask(net.ParseIP(s).To4()).Size()
	return bits == 32 && ones >= 1 && ones <= 31
}

func startJob(w http.ResponseWriter, job *Job) {
	state.mu.Lock()
	if state.job != nil && !state.job.snapshot().Done {
		state.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "Busy: a job is running."})
		return
	}
	if state.settings != nil && !state.settings.snapshot().Done {
		state.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "Busy: settings are being sent."})
		return
	}
	state.job = job
	state.mu.Unlock()
	notify()
	go job.start()
	writeJSON(w, 202, map[string]any{"ok": true, "jobId": job.ID})
}

func handleApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []struct {
			ID    string `json:"id"`
			NewIP string `json:"newIp"`
		} `json:"items"`
		Netmask string `json:"netmask"`
		Gateway string `json:"gateway"`
		// IgnoreClash is the page's "Ignore address clashes" tick-box: the operator has chosen to put a device
		// on an address another one holds.
		IgnoreClash bool `json:"ignoreClash"`
	}
	if err := readJSON(r, &body); err != nil || len(body.Items) == 0 {
		writeJSON(w, 400, map[string]string{"error": "No devices to address."})
		return
	}
	if body.Netmask == "" {
		body.Netmask = "255.255.0.0"
	}
	if body.Gateway == "" {
		body.Gateway = "0.0.0.0"
	}
	if !validMask(body.Netmask) {
		writeJSON(w, 400, map[string]string{"error": "Invalid netmask " + body.Netmask + " (expected something like 255.255.0.0)."})
		return
	}
	if !validIPv4(body.Gateway) {
		writeJSON(w, 400, map[string]string{"error": "Invalid gateway " + body.Gateway + "."})
		return
	}
	var ids []string
	targets := map[string]string{}
	for _, it := range body.Items {
		if why := badNewIP(it.NewIP); why != "" {
			writeJSON(w, 400, map[string]string{"error": why})
			return
		}
		if _, dup := targets[it.ID]; dup {
			writeJSON(w, 400, map[string]string{"error": "Device " + it.ID + " is listed twice."})
			return
		}
		if why := edgeOfSubnet(it.NewIP, body.Netmask); why != "" {
			writeJSON(w, 400, map[string]string{"error": why})
			return
		}
		ids = append(ids, it.ID)
		targets[it.ID] = it.NewIP
	}
	devs, msg := lookup(ids)
	if msg == "" && !body.IgnoreClash {
		msg = addressClash(targets)
	}
	if msg != "" {
		writeJSON(w, 400, map[string]string{"error": msg})
		return
	}
	var items []*Item
	for _, d := range devs {
		items = append(items, &Item{ID: d.ID, Device: d, TargetIP: targets[d.ID]})
	}
	job := newJob("apply", items, notify, &state.scanLock)
	job.seen = mergeSeen
	job.Netmask, job.Gateway = body.Netmask, body.Gateway
	startJob(w, job)
}

func handleReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs          []string `json:"ids"`
		KeepProfiles bool     `json:"keepProfiles"`
		KeepIP       bool     `json:"keepIp"`
	}
	if err := readJSON(r, &body); err != nil || len(body.IDs) == 0 {
		writeJSON(w, 400, map[string]string{"error": "No devices selected."})
		return
	}
	devs, msg := lookup(dedupe(body.IDs))
	if msg != "" {
		writeJSON(w, 400, map[string]string{"error": msg})
		return
	}
	var items []*Item
	for _, d := range devs {
		items = append(items, &Item{ID: d.ID, Device: d})
	}
	job := newJob("reset", items, notify, &state.scanLock)
	job.seen = mergeSeen
	job.KeepProfiles, job.KeepIP = body.KeepProfiles, body.KeepIP
	startJob(w, job)
}

func handlePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs      []string `json:"ids"`
		Username string   `json:"username"`
		Password string   `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	state.mu.Lock()
	job := state.job
	state.mu.Unlock()
	if job == nil {
		writeJSON(w, 400, map[string]string{"error": "No job is running."})
		return
	}
	if body.Username == "" {
		body.Username = "admin"
	}
	go job.retryWithPassword(body.IDs, &Auth{Username: body.Username, Password: body.Password})
	notify()
	writeJSON(w, 202, map[string]bool{"ok": true})
}

// Mount registers the commissioning API and page on mux. Call it once, then
// Start when the server is ready to run.
func Mount(mux *http.ServeMux, opts Options) {
	options = opts
	if opts.Scan != nil {
		scanFor = opts.Scan
	}
	if opts.Reachability != nil {
		reachability = opts.Reachability
	}
	static, _ := fs.Sub(publicFS, "public")
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(snapshot())
	})
	mux.HandleFunc("GET /api/events", handleEvents)
	mux.HandleFunc("POST /api/scan", func(w http.ResponseWriter, r *http.Request) {
		go doScan()
		writeJSON(w, 202, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/topology", func(w http.ResponseWriter, r *http.Request) {
		go readTopology()
		writeJSON(w, 202, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/settings", handleSettings)
	mux.HandleFunc("POST /api/groups/read", handleGroupsRead)
	mux.HandleFunc("GET /api/groups/profiles", handleProfilesList)
	mux.HandleFunc("POST /api/groups/profiles/save", handleProfilesSave)
	mux.HandleFunc("POST /api/groups/profiles/delete", handleProfilesDelete)
	mux.HandleFunc("POST /api/apply", handleApply)
	mux.HandleFunc("POST /api/reset", handleReset)
	mux.HandleFunc("POST /api/job/password", handlePassword)
	mux.HandleFunc("GET /api/device/{id}", handleDevice)
	mux.HandleFunc("POST /api/device/add", handleAddDevice)
	mux.HandleFunc("POST /api/device/{id}/ticked", handleTicked)
	mux.HandleFunc("POST /api/job/cancel", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		job := state.job
		state.mu.Unlock()
		if job != nil {
			job.cancel()
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"error": "Unknown API route"})
	})
	mux.Handle("/", noCache(http.FileServer(http.FS(static))))
}

// Start begins pushing state to open pages and runs the first scan.
func Start() {
	go broadcaster()
	go watchNetwork()
	go doScan()
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// handleTicked records the operator's tick on a device. A device is ticked when it
// first appears; an untick lasts, by device, until this tool quits, whatever scans
// and page reloads happen in between. It only decides what Apply and Factory reset
// select, so it does not stop a job that names the device, such as the one Configure's
// Network dialog runs for its own node.
func handleTicked(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ticked bool `json:"ticked"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	state.mu.Lock()
	if body.Ticked {
		delete(state.unticked, id)
	} else {
		state.unticked[id] = true
	}
	state.mu.Unlock()
	notify()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// busyItem finds an item a running job is working on that matches.
func busyItem(match func(*Item) bool) (*Job, *Item) {
	state.mu.Lock()
	job := state.job
	state.mu.Unlock()
	if job == nil {
		return nil, nil
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	for _, it := range job.Items {
		switch it.Status {
		case "queued", "checking", "sending", "rebooting":
			if match(it) {
				return job, it
			}
		}
	}
	return nil, nil
}

func busyWhy(job *Job) string {
	if job.Kind == "reset" {
		return "Being factory reset by a running job."
	}
	return "Address being changed by a running job."
}

// Locked says whether a running job is working on the device at ip, at either its
// old address or the one it is moving to, and why.
func Locked(ip string) (bool, string) {
	state.mu.Lock()
	shared := sharedCounts(state.devices)[ip] > 1 && checkShared()
	state.mu.Unlock()
	if shared {
		return true, sharedMsg
	}
	job, it := busyItem(func(it *Item) bool { return it.Device.IP == ip || it.TargetIP == ip })
	if it == nil {
		if settingsBusy(func(it *SettingsItem) bool { return it.Device.IP == ip }) {
			return true, "Settings being changed by a running job."
		}
		return false, ""
	}
	return true, busyWhy(job)
}

// handleDevice is what a configure page polls to follow its device: where it is
// now and whether a job has it.
func handleDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	out := map[string]any{"known": false, "locked": false, "dryRun": luminex.DryRun}
	state.mu.Lock()
	for _, d := range state.devices {
		if d.ID == id {
			out["known"], out["ip"], out["reachable"], out["reachReason"] = true, d.IP, d.Reachable, d.ReachReason
			break
		}
	}
	state.mu.Unlock()
	if job, it := busyItem(func(it *Item) bool { return it.ID == id }); it != nil {
		out["locked"], out["why"] = true, busyWhy(job)
	} else if settingsBusy(func(it *SettingsItem) bool { return it.ID == id }) {
		out["locked"], out["why"] = true, "Settings being changed by a running job."
	}
	writeJSON(w, 200, out)
}

// probeJSON reads a JSON object from a device with whatever credentials it takes.
func probeJSON(ip, path string) (map[string]any, *Auth, int, error) {
	candidates := []*Auth{luminex.KnownAuth(ip), {Username: "admin"}} // a nil first candidate means none
	var status int
	for _, a := range candidates {
		r, err := deviceFetch("GET", base(ip)+path, "", "", a, 4*time.Second)
		if err != nil {
			return nil, nil, 0, err
		}
		status = r.Status
		if luminex.Denied(r.Status) {
			continue
		}
		if r.Status != 200 {
			return nil, nil, r.Status, nil
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(r.Text), &m); err != nil {
			return nil, nil, r.Status, fmt.Errorf("%s did not answer with JSON", path)
		}
		return m, a, r.Status, nil
	}
	return nil, nil, status, nil
}

func str(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// deviceAt asks a device what it is. Only LumiNode, LumiCore and GigaCore Gen2 have
// an API that can say.
func deviceAt(ip string) (luminex.Device, string) {
	info, auth, status, err := probeJSON(ip, "/api/deviceinfo")
	if err != nil {
		return luminex.Device{}, fmt.Sprintf("No answer from %s (%s).", ip, err)
	}
	if luminex.Denied(status) {
		return luminex.Device{}, fmt.Sprintf("%s has a password. Use Configure's address box or Scan.", ip)
	}
	var d luminex.Device
	if info != nil && str(info, "type") != "" {
		d = luminex.DeviceFromFields(map[string]string{"product_type": str(info, "type"), "serial": str(info, "serial"),
			"mac": str(info, "mac_address"), "device_name": str(info, "short_name"), "ip_address": ip})
		if n, _, _, err := probeJSON(ip, "/api/ipsettings"); err == nil && n != nil {
			if m := str(n, "netmask"); validMask(m) {
				d.Prefix = maskToCIDR(m)
			}
		}
	} else {
		sw, a, status, err := probeJSON(ip, "/api/device")
		if err != nil {
			return luminex.Device{}, fmt.Sprintf("No answer from %s (%s).", ip, err)
		}
		if luminex.Denied(status) {
			return luminex.Device{}, fmt.Sprintf("%s has a password. Use Scan.", ip)
		}
		if sw == nil || str(sw, "model") == "" {
			return luminex.Device{}, fmt.Sprintf("%s is not a LumiNode, LumiCore or GigaCore Gen2.", ip)
		}
		auth = a
		firmware := ""
		if f, ok := sw["firmware"].(map[string]any); ok {
			firmware = str(f, "active")
		}
		d = luminex.DeviceFromFields(map[string]string{"product_type": str(sw, "model"), "serial": str(sw, "serial"),
			"mac": str(sw, "mac_address"), "device_name": str(sw, "name"), "firmware_version": firmware, "ip_address": ip})
		if actual, ok := sw["actual_ip"].(map[string]any); ok {
			if p, ok := actual["prefix_length"].(float64); ok {
				d.Prefix = int(p)
			}
		}
	}
	if d.ID == "" {
		return luminex.Device{}, fmt.Sprintf("%s gave no serial number.", ip)
	}
	luminex.RememberAuth(ip, auth)
	d.Manual = true
	return d, ""
}

// handleAddDevice lists a device mDNS is not finding.
func handleAddDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IP string `json:"ip"`
	}
	if err := readJSON(r, &body); err != nil || !validIPv4(body.IP) {
		writeJSON(w, 400, map[string]string{"error": "Enter an address like 192.168.12.8."})
		return
	}
	d, msg := deviceAt(body.IP)
	if msg != "" {
		writeJSON(w, 400, map[string]string{"error": msg})
		return
	}
	d = enrich(d)
	d.Manual = true
	state.mu.Lock()
	replaced := false
	for i, cur := range state.devices {
		if luminex.SameDevice(cur, d) {
			if !cur.Manual { // already found by a scan, which knows more
				d = cur
			}
			state.devices[i] = d
			replaced = true
			break
		}
	}
	if !replaced {
		state.devices = append(state.devices, d)
	}
	state.mu.Unlock()
	notify()
	writeJSON(w, 200, map[string]any{"ok": true, "id": d.ID, "model": d.Model})
}
