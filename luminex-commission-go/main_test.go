package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/technickirl/lighting-network-tools/internal/commission"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"golang.org/x/net/websocket"
)

// Both packages on one server: newMux panics if their routes clash, and each page
// has to be reachable where the other expects it. No device is contacted.
func TestBothPagesAndAPIsOnOneServer(t *testing.T) {
	srv := httptest.NewServer(newMux(commission.Options{}))
	defer srv.Close()

	for _, c := range []struct {
		path string
		code int
		want string
	}{
		{"/", 200, `src="/app.js"`},
		{"/", 200, `id="configureView"`},
		{"/", 200, `data-view="address"`},
		{"/app.js", 200, "configureUrl"},
		{"/fixtures.js", 200, "readAllFixtures"},
		{"/sacn.js", 200, "function sacnShown"},
		{"/sacn-out.js", 200, "function sacnOutShown"},
		{"/", 200, `id="sacnOutView"`},
		{"/", 200, `id="sacnView"`},
		{"/api/sacn/universe/0", 400, "Universes run from 1"},
		{"/configure/nodeconfig.js", 200, "function rdmLoad"},
		{"/configure/rdm-fixtures.js", 200, "function rdmPanelHtml"},
		{"/style.css", 200, ".btn-configure"},
		{"/", 200, `id="nodeHost"`},
		{"/node.js", 200, "function openNode"},
		{"/configure/node.html", 200, `data-tab="processblocks"`},
		{"/configure/nodeconfig.js", 200, "function ncOpen"},
		{"/configure/nodeconfig.js", 200, "function renderProcessBlocks"},
		{"/configure/nodeconfig.css", 200, "--amber"},
		{"/api/state", 200, `"configureUrl":"/configure/"`},
		{"/api/state", 200, `"title":"Luminex Commission"`},
		{"/api/node/bad_host!/snapshot", 400, "bad device address"},
		{"/api/nope", 404, "Unknown API route"},
	} {
		res, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != c.code || !strings.Contains(string(b), c.want) {
			t.Errorf("GET %s: %d, want %d with %q", c.path, res.StatusCode, c.code, c.want)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("GET %s: Cache-Control %q", c.path, res.Header.Get("Cache-Control"))
		}
	}

	conn, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/live", "", srv.URL)
	if err != nil {
		t.Fatalf("live socket: %v", err)
	}
	conn.Close()
}

// ── The whole flow, with no network: mDNS is stubbed, and every device connection
// goes to one local stand-in whatever address it was meant for.

const nodeSerial = "LUM553897152"

type benchNode struct {
	mu       sync.Mutex
	password string
	hold     chan struct{} // while set, an IP change waits on it
	seen     []luminex.Device
	onMove   func() // called when an IP change is accepted
	writes   []string
	auths    []string
	srv      *httptest.Server
}

func (n *benchNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u, p, ok := r.BasicAuth()
	if n.password != "" && (!ok || u != "admin" || p != n.password) {
		w.WriteHeader(401)
		return
	}
	if r.Method != "GET" {
		n.mu.Lock()
		n.writes = append(n.writes, r.Method+" "+r.URL.Path)
		n.auths = append(n.auths, p)
		hold, move := n.hold, n.onMove
		n.mu.Unlock()
		if r.URL.Path == "/api/ipsettings" {
			if hold != nil {
				<-hold
			}
			if move != nil {
				move()
			}
		}
		w.WriteHeader(202)
		return
	}
	switch r.URL.Path {
	case "/api/deviceinfo":
		io.WriteString(w, `{"type":"LumiNode12","serial":"`+nodeSerial+`","mac_address":"d0:69:9e:c5:d3:34","short_name":"Bench node","long_name":"Bench node","ID":0,"colors":[]}`)
	case "/api/ipsettings":
		io.WriteString(w, `{"ipaddress":"192.168.12.8","netmask":"255.255.0.0","gateway":"0.0.0.0"}`)
	case "/api/processblock":
		io.WriteString(w, `[{"id":0,"name":"ProcessEngine 1","mode":"REROUTE","inputs":{},"outputs":{},"timeout":0}]`)
	case "/api/processblock/0":
		io.WriteString(w, `{"id":0,"name":"ProcessEngine 1","mode":"REROUTE","inputs":{},"outputs":{},"timeout":0}`)
	case "/api/IO", "/api/dmx/ports", "/api/profile":
		io.WriteString(w, `[]`)
	case "/api/system/uptime":
		io.WriteString(w, `1`)
	default:
		w.WriteHeader(404)
	}
}

func (n *benchNode) setSeen(d ...luminex.Device) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seen = d
}

func (n *benchNode) writesSeen() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.writes...)
}

func scanned(ip string) luminex.Device {
	d := luminex.DeviceFromFields(map[string]string{"product_type": "LumiNode12", "serial": nodeSerial, "device_name": "Bench node", "ip_address": ip + "/16"})
	return d
}

// startBench runs the tool's own server against a stand-in node.
func startBench(t *testing.T, password string) (*httptest.Server, *benchNode) {
	t.Helper()
	luminex.ForgetAllAuthForTests()
	n := &benchNode{password: password}
	n.srv = httptest.NewServer(n)
	t.Cleanup(n.srv.Close)
	target := strings.TrimPrefix(n.srv.URL, "http://")
	luminex.SetDeviceDialForTests(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, target)
	})
	t.Cleanup(func() { luminex.SetDeviceDialForTests((&net.Dialer{}).DialContext) })
	srv := httptest.NewServer(newMux(commission.Options{
		Scan: func(time.Duration) ([]luminex.Device, error) {
			n.mu.Lock()
			defer n.mu.Unlock()
			return append([]luminex.Device(nil), n.seen...), nil
		},
		Reachability: func(string, int) luminex.Reach { return luminex.Reach{Reachable: true, Via: "test"} },
	}))
	t.Cleanup(srv.Close)
	return srv, n
}

func send(t *testing.T, method, url, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("never: %s", what)
}

func scanUntilListed(t *testing.T, srv *httptest.Server, ip string) {
	t.Helper()
	send(t, "POST", srv.URL+"/api/scan", "{}")
	eventually(t, "device listed at "+ip, func() bool {
		_, d := send(t, "GET", srv.URL+"/api/device/"+nodeSerial, "")
		return d["known"] == true && d["ip"] == ip
	})
}

// /configure/ was a page of its own before the Node view: an old bookmark or
// open tab lands on the same node in the device list's page.
func TestOldConfigureAddressGoesToTheNodeView(t *testing.T) {
	srv := httptest.NewServer(newMux(commission.Options{}))
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for path, want := range map[string]string{
		"/configure/":                         "/",
		"/configure/?ip=192.168.1.73&id=LUM1": "/#id=LUM1&node=192.168.1.73",
	} {
		res, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusFound || res.Header.Get("Location") != want {
			t.Errorf("GET %s: %d to %q, want 302 to %q", path, res.StatusCode, res.Header.Get("Location"), want)
		}
	}
}

// Address a node, get locked out while the job runs, then configure it at its new
// address: the sequence an operator actually does.
func TestAddressLockThenConfigureAtNewAddress(t *testing.T) {
	srv, n := startBench(t, "")
	n.setSeen(scanned("192.168.112.8"))
	scanUntilListed(t, srv, "192.168.112.8")

	if code, _ := send(t, "GET", srv.URL+"/api/node/192.168.112.8/snapshot", ""); code != 200 {
		t.Fatalf("snapshot before the job: %d", code)
	}

	release := make(chan struct{})
	n.mu.Lock()
	n.hold = release
	n.onMove = func() { n.setSeen(scanned("192.168.112.9")) }
	n.mu.Unlock()
	if code, body := send(t, "POST", srv.URL+"/api/apply", `{"items":[{"id":"`+nodeSerial+`","newIp":"192.168.112.9"}]}`); code != 202 {
		t.Fatalf("apply: %d %v", code, body)
	}
	eventually(t, "device locked", func() bool {
		_, d := send(t, "GET", srv.URL+"/api/device/"+nodeSerial, "")
		return d["locked"] == true && d["why"] != ""
	})

	// Writes wait, at the old address and the new one. Reads still work.
	for _, ip := range []string{"192.168.112.8", "192.168.112.9"} {
		for _, c := range [][2]string{{"PATCH", "/processblock/0"}, {"POST", "/identify"}, {"POST", "/reboot"}, {"POST", "/quicksetup"}, {"PUT", "/ipsettings"}} {
			code, out := send(t, c[0], srv.URL+"/api/node/"+ip+c[1], `{"name":"x"}`)
			if code != 409 || out["locked"] != true {
				t.Errorf("%s %s at %s during the job: %d %v", c[0], c[1], ip, code, out)
			}
		}
	}
	if code, _ := send(t, "GET", srv.URL+"/api/node/192.168.112.8/snapshot", ""); code != 200 {
		t.Errorf("read during the job: %d", code)
	}
	for _, w := range n.writesSeen() {
		if strings.Contains(w, "processblock") || strings.Contains(w, "identify") || strings.Contains(w, "reboot") {
			t.Errorf("a refused write reached the device: %s", w)
		}
	}

	close(release)
	eventually(t, "device at its new address, unlocked", func() bool {
		_, d := send(t, "GET", srv.URL+"/api/device/"+nodeSerial, "")
		return d["locked"] == false && d["ip"] == "192.168.112.9"
	})

	// The same device is now configurable at the new address.
	if code, out := send(t, "GET", srv.URL+"/api/node/192.168.112.9/snapshot", ""); code != 200 || out["dryRun"] != false {
		t.Errorf("snapshot at the new address: %d %v", code, out["dryRun"])
	}
	if code, out := send(t, "PATCH", srv.URL+"/api/node/192.168.112.9/processblock/0", `{"name":"Renamed"}`); code != 200 {
		t.Errorf("edit at the new address: %d %v", code, out)
	}
	writes := n.writesSeen()
	if writes[len(writes)-1] != "PUT /api/processblock/0" {
		t.Errorf("writes = %v", writes)
	}
}

// One password, entered once, works for the configure page and for a job, and still
// works after that job has moved the device.
func TestPasswordIsSharedAndFollowsTheDevice(t *testing.T) {
	srv, n := startBench(t, "pw")
	n.mu.Lock()
	n.onMove = func() { n.setSeen(scanned("192.168.113.9")) }
	n.mu.Unlock()
	n.setSeen(scanned("192.168.113.8"))
	scanUntilListed(t, srv, "192.168.113.8")

	code, out := send(t, "GET", srv.URL+"/api/node/192.168.113.8/snapshot", "")
	if code != 401 || out["needsPassword"] != true {
		t.Fatalf("snapshot without a password: %d %v", code, out)
	}
	if code, _ := send(t, "POST", srv.URL+"/api/node/192.168.113.8/password", `{"password":"wrong"}`); code != 401 {
		t.Errorf("wrong password: %d", code)
	}
	if code, out := send(t, "POST", srv.URL+"/api/node/192.168.113.8/password", `{"password":"pw"}`); code != 200 {
		t.Fatalf("right password: %d %v", code, out)
	}
	if code, _ := send(t, "GET", srv.URL+"/api/node/192.168.113.8/snapshot", ""); code != 200 {
		t.Errorf("snapshot with the password: %d", code)
	}

	if code, body := send(t, "POST", srv.URL+"/api/apply", `{"items":[{"id":"`+nodeSerial+`","newIp":"192.168.113.9"}]}`); code != 202 {
		t.Fatalf("apply: %d %v", code, body)
	}
	eventually(t, "device moved without asking again", func() bool {
		_, d := send(t, "GET", srv.URL+"/api/device/"+nodeSerial, "")
		return d["locked"] == false && d["ip"] == "192.168.113.9"
	})
	if code, _ := send(t, "GET", srv.URL+"/api/node/192.168.113.9/snapshot", ""); code != 200 {
		t.Errorf("snapshot at the new address: %d", code)
	}
}

func TestAddByAddressStaysListedAcrossScans(t *testing.T) {
	srv, n := startBench(t, "")
	n.setSeen() // mDNS finds nothing
	send(t, "POST", srv.URL+"/api/scan", "{}")
	eventually(t, "scan finished", func() bool {
		_, s := send(t, "GET", srv.URL+"/api/state", "")
		return s["scanning"] == false
	})

	if code, _ := send(t, "POST", srv.URL+"/api/device/add", `{"ip":"not an address"}`); code != 400 {
		t.Errorf("bad address: %d", code)
	}
	code, out := send(t, "POST", srv.URL+"/api/device/add", `{"ip":"192.168.114.8"}`)
	if code != 200 || out["id"] != nodeSerial {
		t.Fatalf("add: %d %v", code, out)
	}
	_, d := send(t, "GET", srv.URL+"/api/device/"+nodeSerial, "")
	if d["known"] != true || d["ip"] != "192.168.114.8" {
		t.Errorf("after add: %v", d)
	}

	send(t, "POST", srv.URL+"/api/scan", "{}")
	time.Sleep(100 * time.Millisecond)
	eventually(t, "scan finished", func() bool {
		_, s := send(t, "GET", srv.URL+"/api/state", "")
		return s["scanning"] == false
	})
	if _, d := send(t, "GET", srv.URL+"/api/device/"+nodeSerial, ""); d["known"] != true {
		t.Errorf("a scan that did not see it dropped it: %v", d)
	}
}

// Access from other devices is on at start unless --no-lan.
func TestLANAtStartDefaultsOn(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{"--no-open"}, true},
		{[]string{"--lan"}, true},
		{[]string{"--no-lan"}, false},
		{[]string{"--no-open", "--no-lan", "--port", "3801"}, false},
		{[]string{"--no-lan", "--lan"}, true},
	} {
		if got := lanAtStart(c.args); got != c.want {
			t.Errorf("lanAtStart(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

// --save-pin saves the PIN that access then turns on with at start, and --lan with
// no saved PIN prints the random one (the Pi's screen is the only other place).
func TestSavePINThenLANAtStart(t *testing.T) {
	pinFile := t.TempDir() + "/lan-pin.json"
	t.Setenv("LUMINEX_LAN_PIN_FILE", pinFile)
	newLAN := func() *luminex.LANAccess {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		lan := luminex.NewLANAccess(port, http.NotFoundHandler())
		lan.SetInterfacesForTests(func() []luminex.Iface { return []luminex.Iface{{Name: "lo0", Address: "127.0.0.1"}} })
		lan.SetPINFile(luminex.DefaultPINFile("luminex-commission"))
		return lan
	}

	if code := savePIN("12345"); code == 0 {
		t.Error("savePIN accepted five digits")
	}

	var out strings.Builder
	lan := newLAN()
	startLAN(lan, &out)
	st := lan.State()
	if !st.Enabled || st.SavedPIN != "" || !strings.Contains(out.String(), "PIN "+st.PIN+" (random") {
		t.Errorf("no saved PIN: enabled %v, printed %q", st.Enabled, out.String())
	}
	if !strings.Contains(out.String(), "Open http://127.0.0.1:") {
		t.Errorf("address not printed: %q", out.String())
	}
	lan.Disable()

	if code := savePIN("424242"); code != 0 {
		t.Fatalf("savePIN: exit %d", code)
	}
	out.Reset()
	lan = newLAN()
	startLAN(lan, &out)
	st = lan.State()
	if !st.Enabled || st.PIN != "424242" || strings.Contains(out.String(), "424242") {
		t.Errorf("saved PIN: enabled %v, PIN %q, printed %q", st.Enabled, st.PIN, out.String())
	}
	lan.Disable()

	if code := savePIN(""); code != 0 || newLAN().State().SavedPIN != "" {
		t.Errorf("savePIN(\"\"): exit %d, still saved", code)
	}
}
