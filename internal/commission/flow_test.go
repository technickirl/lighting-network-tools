package commission

// Whole jobs driven through the HTTP API the page uses, against a local server
// standing in for the device. mDNS is stubbed, so nothing is advertised or sent
// on a real network.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

const flowID = "LUM553897152"

// network is what the stubbed mDNS scan sees.
type network struct {
	mu   sync.Mutex
	seen []luminex.Device
}

func (n *network) set(d ...luminex.Device) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seen = d
}

func (n *network) scan(time.Duration) ([]luminex.Device, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]luminex.Device(nil), n.seen...), nil
}

// flowNode is a LumiNode as a scan reports it.
func flowNode(ip, name string) luminex.Device {
	return luminex.Device{ID: flowID, Instance: flowID, IP: ip, Prefix: 16, Family: "luminode", Model: "LumiNode12", ProductType: "LumiNode12", Name: name}
}

// startFlow mounts the API, stubs the machine and network, and clears any earlier job.
func startFlow(t *testing.T) (*httptest.Server, *network) {
	t.Helper()
	n := &network{}
	oldScan, oldReach := scanFor, reachability
	scanFor = n.scan
	reachability = func(string, int) luminex.Reach { return luminex.Reach{Reachable: true, Via: "test"} }
	t.Cleanup(func() { scanFor, reachability = oldScan, oldReach })
	state.mu.Lock()
	state.devices, state.job, state.scanning, state.scanError = nil, nil, false, ""
	state.unticked = map[string]bool{}
	state.mu.Unlock()
	mux := http.NewServeMux()
	Mount(mux, Options{Version: "9.9.9", Title: "Test tool", ConfigureURL: "/configure/"})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, n
}

func postJSON(t *testing.T, url, body string) (int, string) {
	t.Helper()
	res, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b := make([]byte, 4096)
	n, _ := res.Body.Read(b)
	return res.StatusCode, string(b[:n])
}

func apiState(t *testing.T, srv *httptest.Server) Snapshot {
	t.Helper()
	res, err := http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var s Snapshot
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// waitItem polls the API until the job's only item reaches a final or asked-for status.
func waitItem(t *testing.T, srv *httptest.Server, want string) Item {
	t.Helper()
	var last Snapshot
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		last = apiState(t, srv)
		if last.Job != nil && len(last.Job.Items) == 1 && last.Job.Items[0].Status == want {
			return last.Job.Items[0]
		}
	}
	t.Fatalf("item never reached %q: %+v", want, last.Job)
	return Item{}
}

func TestMountServesPageAndAPI(t *testing.T) {
	srv, _ := startFlow(t)
	for path, want := range map[string]string{"/": "Luminex IP Setup", "/app.js": "EventSource", "/style.css": "--accent"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 1<<16)
		n, _ := res.Body.Read(b)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(b[:n]), want) {
			t.Errorf("GET %s: %d, no %q", path, res.StatusCode, want)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("GET %s: Cache-Control %q", path, res.Header.Get("Cache-Control"))
		}
	}
	if s := apiState(t, srv); s.Version != "9.9.9" || s.Title != "Test tool" || s.ConfigureURL != "/configure/" || s.Devices == nil {
		t.Errorf("state = %+v", s)
	}
	res, err := http.Get(srv.URL + "/api/nope")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("unknown API route: %d", res.StatusCode)
	}
	for _, path := range []string{"/api/apply", "/api/reset"} {
		if code, _ := postJSON(t, srv.URL+path, `{}`); code != 400 {
			t.Errorf("POST %s with nothing selected: %d", path, code)
		}
	}
}

func TestApplyThroughTheAPI(t *testing.T) {
	srv, n := startFlow(t)
	dev := newWire(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			w.Write([]byte(`{"ID":7,"colors":[1,2,3],"short_name":"old"}`))
		case r.URL.Path == "/api/ipsettings":
			n.set(flowNode("192.168.12.3", "old")) // reappears at the new address
			w.WriteHeader(202)
		default:
			w.WriteHeader(200)
		}
	})
	n.set(flowNode(dev.addr(), "old"))
	doScan()

	// Addresses no device can take are refused before anything is sent.
	for _, bad := range []string{"0.0.0.0", "127.0.0.1", "224.0.0.251", "255.255.255.255", "0.1.2.3", "not-an-ip"} {
		if code, body := postJSON(t, srv.URL+"/api/apply", `{"items":[{"id":"`+flowID+`","newIp":"`+bad+`"}]}`); code != 400 {
			t.Errorf("apply to %s: %d %s", bad, code, body)
		}
	}

	code, body := postJSON(t, srv.URL+"/api/apply", `{"items":[{"id":"`+flowID+`","newIp":"192.168.12.3"}]}`)
	if code != 202 {
		t.Fatalf("apply: %d %s", code, body)
	}
	it := waitItem(t, srv, "done")
	if it.Label != "Addressed ✓" || it.NewIP != "192.168.12.3" || !strings.Contains(it.Detail, "Now at 192.168.12.3") {
		t.Errorf("item = %+v", it)
	}

	var writes []seenReq
	for _, r := range dev.requests() {
		if r.Method != "GET" {
			writes = append(writes, r)
		}
	}
	want := []seenReq{
		{"PUT", "/api/ipsettings", "application/json", `{"ipaddress":"192.168.12.3","netmask":"255.255.0.0","gateway":"0.0.0.0"}`, ""},
	}
	if len(writes) != len(want) || writes[0] != want[0] {
		t.Errorf("writes: the IP change only\n got %+v\nwant %+v", writes, want)
	}

	// The device list follows it, so the next action goes to the new address.
	s := apiState(t, srv)
	if len(s.Devices) != 1 || s.Devices[0].IP != "192.168.12.3" {
		t.Errorf("device list did not follow: %+v", s.Devices)
	}
	if s.Job == nil || !s.Job.Done || s.Job.Counts.OK != 1 {
		t.Errorf("job = %+v", s.Job)
	}
}

func TestResetThroughTheAPI(t *testing.T) {
	srv, n := startFlow(t)
	dev := newWire(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			n.set(flowNode("2.0.0.9", "LumiNode12")) // back on its factory-default address
			w.WriteHeader(204)
		}
	})
	n.set(flowNode(dev.addr(), "old"))
	doScan()
	if code, body := postJSON(t, srv.URL+"/api/reset", `{"ids":["`+flowID+`"],"keepProfiles":true}`); code != 202 {
		t.Fatalf("reset: %d %s", code, body)
	}
	it := waitItem(t, srv, "done")
	if it.Label != "Reset ✓" || it.NewIP != "2.0.0.9" || !strings.Contains(it.Detail, "Back on the network at 2.0.0.9") {
		t.Errorf("item = %+v", it)
	}
	var writes []seenReq
	for _, r := range dev.requests() {
		if r.Method != "GET" {
			writes = append(writes, r)
		}
	}
	if len(writes) != 1 || writes[0].Method != "POST" || writes[0].Path != "/api/reset" || writes[0].Body != `{"keep_ip_settings":false,"keep_user_profiles":true}` {
		t.Errorf("writes = %+v", writes)
	}
}

func TestPasswordFlowThroughTheAPI(t *testing.T) {
	srv, n := startFlow(t)
	dev := newWire(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != basicAdminPw {
			w.WriteHeader(401)
			return
		}
		if r.Method == "PUT" {
			n.set(flowNode("192.168.12.3", "old"))
			w.WriteHeader(202)
		}
	})
	n.set(flowNode(dev.addr(), "old"))
	doScan()
	if code, body := postJSON(t, srv.URL+"/api/apply", `{"items":[{"id":"`+flowID+`","newIp":"192.168.12.3"}]}`); code != 202 {
		t.Fatalf("apply: %d %s", code, body)
	}
	waitItem(t, srv, "needs_password")
	for _, r := range dev.requests() {
		if r.Method != "GET" {
			t.Fatalf("wrote before it had the password: %+v", r)
		}
	}

	if code, body := postJSON(t, srv.URL+"/api/job/password", `{"ids":["`+flowID+`"],"password":"pw"}`); code != 202 {
		t.Fatalf("password: %d %s", code, body)
	}
	if it := waitItem(t, srv, "done"); it.NewIP != "192.168.12.3" {
		t.Errorf("item = %+v", it)
	}
	var writes []seenReq
	for _, r := range dev.requests() {
		if r.Method != "GET" {
			writes = append(writes, r)
		}
	}
	if len(writes) != 1 || writes[0].Path != "/api/ipsettings" || writes[0].Authorization != basicAdminPw {
		t.Errorf("writes = %+v", writes)
	}
}

func TestOnlyOneJobAtATime(t *testing.T) {
	srv, n := startFlow(t)
	n.set(flowNode("127.0.0.1", "old"))
	doScan()
	// A job that has not finished, never started so nothing runs behind the test.
	running := newJob("reset", []*Item{{ID: flowID, Device: flowNode("127.0.0.1", "old")}}, func() {}, &state.scanLock)
	state.mu.Lock()
	state.job = running
	state.mu.Unlock()
	if code, _ := postJSON(t, srv.URL+"/api/apply", `{"items":[{"id":"`+flowID+`","newIp":"192.168.12.3"}]}`); code != 409 {
		t.Errorf("apply while a job runs: %d", code)
	}
	if code, _ := postJSON(t, srv.URL+"/api/reset", `{"ids":["`+flowID+`"]}`); code != 409 {
		t.Errorf("reset while a job runs: %d", code)
	}
}

func TestDryRunThroughTheAPI(t *testing.T) {
	setDry(t, true)
	srv, n := startFlow(t)
	dev := newWire(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ID":7,"colors":[]}`)) })
	n.set(flowNode(dev.addr(), "old"))
	doScan()
	if code, body := postJSON(t, srv.URL+"/api/apply", `{"items":[{"id":"`+flowID+`","newIp":"192.168.12.3"}]}`); code != 202 {
		t.Fatalf("apply: %d %s", code, body)
	}
	it := waitItem(t, srv, "dry_run")
	if !strings.Contains(it.Detail, "/api/ipsettings") {
		t.Errorf("detail should show the write: %q", it.Detail)
	}
	for _, r := range dev.requests() {
		if r.Method != "GET" {
			t.Errorf("write reached the device under DRY_RUN: %+v", r)
		}
	}
	if s := apiState(t, srv); !s.DryRun || s.Devices[0].IP != dev.addr() {
		t.Errorf("state = dryRun %v, devices %+v", s.DryRun, s.Devices)
	}
}

func TestUntickLastsAcrossScansUntilRetick(t *testing.T) {
	srv, n := startFlow(t)
	n.set(flowNode("192.168.12.8", "Node"))
	doScan()
	if s := apiState(t, srv); len(s.Unticked) != 0 {
		t.Fatalf("a device should be ticked when it first appears: %v", s.Unticked)
	}

	if code, _ := postJSON(t, srv.URL+"/api/device/"+flowID+"/ticked", `{"ticked":false}`); code != 200 {
		t.Fatalf("untick: %d", code)
	}
	// Still unticked after a rescan, after leaving the network and coming back, and
	// after moving to another address.
	doScan()
	n.set()
	doScan()
	n.set(flowNode("192.168.12.9", "Node"))
	doScan()
	if s := apiState(t, srv); len(s.Unticked) != 1 || s.Unticked[0] != flowID {
		t.Errorf("unticked after scans = %v", s.Unticked)
	}

	if code, _ := postJSON(t, srv.URL+"/api/device/"+flowID+"/ticked", `{"ticked":true}`); code != 200 {
		t.Fatalf("retick: %d", code)
	}
	if s := apiState(t, srv); len(s.Unticked) != 0 {
		t.Errorf("unticked after retick = %v", s.Unticked)
	}
}

// The page checks these too, but the Configure page's Network dialog and a second device post without it.
func TestApplyRefusesAddressClashes(t *testing.T) {
	srv, _ := startFlow(t)
	a := luminex.Device{ID: "A", Serial: "A", IP: "192.168.1.10", Prefix: 16, Family: "luminode", APIKind: "luminode"}
	b := luminex.Device{ID: "B", Serial: "B", Name: "Stage switch", IP: "192.168.1.20", Prefix: 16, Family: "gigacore", APIKind: "gen2"}
	state.mu.Lock()
	state.devices = []luminex.Device{a, b}
	state.mu.Unlock()
	for name, c := range map[string]struct{ body, want string }{
		"another device's address": {`{"items":[{"id":"A","newIp":"192.168.1.20"}]}`, "in use by Stage switch"},
		"two devices, one address": {`{"items":[{"id":"A","newIp":"192.168.1.30"},{"id":"B","newIp":"192.168.1.30"}]}`, "would both get"},
		"network address":          {`{"items":[{"id":"A","newIp":"192.168.1.0"}],"netmask":"255.255.255.0"}`, "network address"},
		"broadcast address":        {`{"items":[{"id":"A","newIp":"192.168.255.255"}],"ignoreClash":true}`, "broadcast address"},
	} {
		if code, body := postJSON(t, srv.URL+"/api/apply", c.body); code != 400 || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if s := apiState(t, srv); s.Job != nil {
		t.Fatalf("a refused apply started a job: %+v", s.Job)
	}
	// .255 is an ordinary address in a /16, and the tick-box lets a clash through.
	if why := edgeOfSubnet("192.168.1.255", "255.255.0.0"); why != "" {
		t.Error(why)
	}
	oldDry := luminex.DryRun
	luminex.DryRun = true
	t.Cleanup(func() { luminex.DryRun = oldDry })
	if msg := addressClash(map[string]string{"A": "192.168.1.10"}); msg != "" { // keeping its own address (a mask change)
		t.Error(msg)
	}
}

func TestSerialAtKnowsOnlyAnUnsharedAddress(t *testing.T) {
	startFlow(t)
	state.mu.Lock()
	state.devices = []luminex.Device{{ID: "A", Serial: "A", IP: "192.168.1.10"}, {ID: "B", Serial: "B", IP: "192.168.1.20"}, {ID: "C", Serial: "C", IP: "192.168.1.20"}}
	state.mu.Unlock()
	if got := SerialAt("192.168.1.10"); got != "A" {
		t.Errorf("one device at the address: %q", got)
	}
	if got := SerialAt("192.168.1.20") + SerialAt("192.168.1.99"); got != "" {
		t.Errorf("a shared or unknown address has no one serial: %q", got)
	}
}

// A page's script error reaches the tool's log on one line, however it is sent.
func TestClientLogTakesAPageError(t *testing.T) {
	srv, _ := startFlow(t)
	if code, body := postJSON(t, srv.URL+"/api/client-log", `{"message":"openNode is not defined\n(app.js:380)","agent":"CriOS"}`); code != 200 {
		t.Errorf("%d %s", code, body)
	}
	if code, _ := postJSON(t, srv.URL+"/api/client-log", `{"message":"  "}`); code != 400 {
		t.Errorf("an empty message: %d", code)
	}
}
