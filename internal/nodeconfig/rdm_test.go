package nodeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/technickirl/lighting-network-tools/internal/artnet"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

// rdmNode is a LumiNode with two DMX ports: port 1 an output fed by sACN U1,
// port 2 idle. Turning rdm on sets rdm_universe to the input's universe, as a
// real LumiNode 12 (v2.9.1) did.
type rdmNode struct {
	mu    sync.Mutex
	ios   []obj
	puts  []string
	fake  *artnet.FakeNode
	extra []string // UIDs the node lists over HTTP but its Art-Net table leaves out
	port2 []string // what the node lists for port 2
}

func (n *rdmNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	io0 := func(id float64) obj {
		for _, io := range n.ios {
			if io["id"] == id {
				return io
			}
		}
		return nil
	}
	switch p := r.URL.Path; {
	case r.Method == "GET" && p == "/api/IO":
		reply(n.ios)
	case r.Method == "GET" && strings.HasSuffix(p, "/artrdm_candidates"):
		reply([]obj{{"io_class": "sacn", "io_type": "input", "id": 0, "universe": 1}})
	case r.Method == "GET" && strings.HasPrefix(p, "/api/IO/"):
		reply(io0(map[string]float64{"/api/IO/100000": 100000, "/api/IO/100001": 100001}[p]))
	case r.Method == "PUT" && p == "/api/IO/100000":
		b, _ := io.ReadAll(r.Body)
		n.puts = append(n.puts, string(b))
		var upd obj
		_ = json.Unmarshal(b, &upd)
		io := io0(100000)
		for k, v := range upd {
			io[k] = v
		}
		if io["rdm"] == true {
			io["rdm_universe"] = float64(1)
		}
		w.WriteHeader(202)
	case r.Method == "GET" && p == "/api/rdm/0/connected_devices":
		uids := []string{}
		if io0(100000)["rdm"] == true {
			n.fake.Lock()
			for _, f := range n.fake.Ports[1] {
				b := f.UID.Bytes()
				uids = append(uids, strings.ToUpper(strings.Join([]string{hx(b[0]), hx(b[1]), hx(b[2]), hx(b[3]), hx(b[4]), hx(b[5])}, ":")))
			}
			n.fake.Unlock()
			uids = append(uids, n.extra...)
		}
		reply(uids)
	case r.Method == "GET" && p == "/api/rdm/1/connected_devices":
		reply(append([]string{}, n.port2...))
	case r.Method == "GET" && p == "/api/rdm/uid":
		reply([]obj{{"port": 0, "uid": "4F:75:00:0D:C1:13", "manufacturer_label": "Creamsource", "device_model_description": "Vortex 8S",
			"device_label": "CSV-8S 901395", "dmx_footprint": 10, "dmx_start_address": 1, "dmx_personality": 1, "personality_count": 75,
			"dmx_personality_description": []any{}, "product_category": 257, "software_version_label": "3.1.0", "sensor_count": 5}})
	case r.Method == "POST" && p == "/api/dmx/ports/0/force_discovery":
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			w.WriteHeader(415) // as a real LumiNode 12 (v2.9.1) does
			return
		}
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
	}
}

func hx(b byte) string { return string("0123456789ABCDEF"[b>>4]) + string("0123456789ABCDEF"[b&15]) }

func newRDMNode(t *testing.T) (*rdmNode, *httptest.Server) {
	t.Helper()
	n := &rdmNode{ios: []obj{
		{"io_class": "sacn", "id": float64(0), "io_type": "input", "universe": float64(1)},
		{"io_class": "dmx", "name": "Port 1", "id": float64(100000), "io_type": "output", "port_number": float64(0), "rdm": false,
			"interweaving": true, "adaptive_discovery": false, "rdm_universe": float64(-1), "follow": false, "backup": false,
			"stream_activity_time": float64(6000), "stream_activity_time_custom": false},
		{"io_class": "dmx", "name": "Port 2", "id": float64(100001), "io_type": "idle", "port_number": float64(1), "rdm": false, "rdm_universe": float64(-1)},
	}}
	a := &artnet.FakeFixture{UID: 0x4F75000DC113, Manufacturer: "Creamsource", Model: "Vortex 8S", ModelID: 0x0301, Category: 0x0101,
		Label: "CSV-8S 901395", Software: "3.1.0", Start: 1, Personality: 1, Personalities: []artnet.FakePersonality{{Footprint: 10, Name: "Direct"}}, Sensors: 5}
	b := &artnet.FakeFixture{UID: 0x4F75000DC114, Manufacturer: "Creamsource", Model: "Vortex 8S", ModelID: 0x0301, Category: 0x0101,
		Label: "CSV-8S 901396", Software: "3.1.0", Start: 5, Personality: 2, Personalities: []artnet.FakePersonality{{Footprint: 10, Name: "Direct"}, {Footprint: 16, Name: "HSI"}}}
	enabled := func(pa artnet.PortAddress) bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.ios[1]["rdm"] == true && pa == 1
	}
	fake, err := artnet.ServeFake("127.0.0.1:0", map[artnet.PortAddress][]*artnet.FakeFixture{1: {b, a}}, enabled)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fake.Close() })
	n.fake = fake

	ctl, err := artnet.Open("127.0.0.1:0", fake.Addr().Port)
	if err != nil {
		t.Fatal(err)
	}
	ctl.Timeout = 300 * time.Millisecond
	t.Cleanup(func() { ctl.Close() })
	old := rdmController
	rdmController = func() (*artnet.Controller, error) { return ctl, nil }
	t.Cleanup(func() { rdmController = old })

	dev := httptest.NewServer(n)
	t.Cleanup(dev.Close)
	target := strings.TrimPrefix(dev.URL, "http://")
	luminex.SetDeviceDialForTests(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, target)
	})
	t.Cleanup(func() { luminex.SetDeviceDialForTests((&net.Dialer{}).DialContext) })
	mux := http.NewServeMux()
	Mount(mux, Options{})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	resetRDMStore := func() { rdmStore.Lock(); rdmStore.m = map[string]*rdmNodeState{}; rdmStore.Unlock() }
	resetRDMStore()
	t.Cleanup(resetRDMStore)
	return n, srv
}

// The routes take the node's bare address; Art-Net goes to 127.0.0.1, where the fake is.
func rdmCall(t *testing.T, srv *httptest.Server, method, path, body string) (int, obj) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+"/api/node/127.0.0.1"+path, strings.NewReader(body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out obj
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestRDMTurnOnAndReadFixtures(t *testing.T) {
	n, srv := newRDMNode(t)

	code, out := rdmCall(t, srv, "GET", "/rdm", "")
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	ports := out["ports"].([]any)
	p1 := ports[0].(map[string]any)
	if len(ports) != 2 || p1["port"] != float64(1) || p1["rdm"] != false || p1["source"] != "sACN U1" || p1["count"] != float64(-1) {
		t.Fatalf("ports %v", ports)
	}
	if code, out = rdmCall(t, srv, "GET", "/rdm/1/fixtures", ""); code != 502 || !strings.Contains(out["error"].(string), "RDM is off") {
		t.Errorf("fixtures with RDM off: %d %v", code, out)
	}
	if code, out = rdmCall(t, srv, "PUT", "/rdm/2", `{"on":true}`); code != 502 || !strings.Contains(out["error"].(string), "not an output") {
		t.Errorf("RDM on for an idle port: %d %v", code, out)
	}

	if code, out = rdmCall(t, srv, "PUT", "/rdm/1", `{"on":true}`); code != 200 {
		t.Fatalf("RDM on: %d %v", code, out)
	}
	p1 = out["ports"].([]any)[0].(map[string]any)
	if p1["rdm"] != true || p1["universe"] != float64(1) || p1["count"] != float64(2) {
		t.Errorf("after RDM on: %v", p1)
	}
	// The whole IO went back with only rdm changed.
	var sent obj
	if len(n.puts) != 1 || json.Unmarshal([]byte(n.puts[0]), &sent) != nil || sent["rdm"] != true || sent["stream_activity_time"] != float64(6000) || sent["interweaving"] != true {
		t.Errorf("PUT %v", n.puts)
	}

	code, out = rdmCall(t, srv, "GET", "/rdm/1/fixtures", "")
	if code != 200 || out["source"] != "artnet" || out["universe"] != float64(1) {
		t.Fatalf("fixtures: %d %v", code, out)
	}
	fx := out["fixtures"].([]any)
	if len(fx) != 2 {
		t.Fatalf("fixtures %v", fx)
	}
	a, b := fx[0].(map[string]any), fx[1].(map[string]any) // sorted by address
	if a["uid"] != "4F75:000DC113" || a["start"] != float64(1) || a["footprint"] != float64(10) || a["label"] != "CSV-8S 901395" ||
		a["manufacturer"] != "Creamsource" || a["model"] != "Vortex 8S" || a["software"] != "3.1.0" || a["sensors"] != float64(5) ||
		a["personalityName"] != "Direct" || a["category"] != "Fixture" {
		t.Errorf("first fixture %v", a)
	}
	if b["start"] != float64(5) || b["footprint"] != float64(16) || b["personality"] != float64(2) || b["personalities"] != float64(2) || b["personalityName"] != "HSI" {
		t.Errorf("second fixture %v", b)
	}
	// 1-10 and 5-20 share channels 5-10.
	if ov, _ := a["overlaps"].([]any); len(ov) != 1 || ov[0] != "4F75:000DC114" {
		t.Errorf("overlaps %v", a["overlaps"])
	}

	if code, _ = rdmCall(t, srv, "POST", "/rdm/1/discovery", ""); code != 200 {
		t.Errorf("discovery: %d", code)
	}
	if code, out = rdmCall(t, srv, "PUT", "/rdm/1", `{"on":false}`); code != 200 || out["ports"].([]any)[0].(map[string]any)["rdm"] != false {
		t.Errorf("RDM off: %d %v", code, out)
	}
}

func TestRDMFallsBackToWhatTheNodeOverheard(t *testing.T) {
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	rdmController = func() (*artnet.Controller, error) {
		return nil, errors.New("Art-Net port 6454 is in use by another program on this computer")
	}
	code, out := rdmCall(t, srv, "GET", "/rdm/1/fixtures", "")
	if code != 200 || out["source"] != "overheard" || !strings.Contains(out["note"].(string), "in use by another program") {
		t.Fatalf("%d %v", code, out)
	}
	fx := out["fixtures"].([]any)
	if len(fx) != 1 || fx[0].(map[string]any)["uid"] != "4F75:000DC113" || fx[0].(map[string]any)["start"] != float64(1) {
		t.Errorf("fixtures %v", fx)
	}
	code, out = rdmCall(t, srv, "GET", "/rdm", "")
	if code != 200 || out["artnet"].(map[string]any)["ok"] != false {
		t.Errorf("artnet state %v", out["artnet"])
	}
}

func TestRDMOnIsNotSentUnderDryRun(t *testing.T) {
	setDry(t, true)
	n, srv := newRDMNode(t)
	if code, out := rdmCall(t, srv, "PUT", "/rdm/1", `{"on":true}`); code != 200 || out["dryRun"] != true {
		t.Fatalf("%d %v", code, out)
	}
	if len(n.puts) != 0 {
		t.Errorf("a PUT reached the node under DRY_RUN: %v", n.puts)
	}
}

func TestMarkOverlaps(t *testing.T) {
	at := func(s int) *int { return &s }
	fx := []rdmFixture{
		{UID: "C", Start: at(505), Footprint: 10},
		{UID: "B", Start: nil, Footprint: 0},
		{UID: "A", Start: at(1), Footprint: 10},
		{UID: "D", Start: at(11), Footprint: 4},
	}
	markOverlaps(fx)
	order := ""
	for _, f := range fx {
		order += f.UID
	}
	if order != "ADCB" {
		t.Errorf("order %s", order)
	}
	for _, f := range fx {
		if len(f.Overlaps) != 0 {
			t.Errorf("%s overlaps %v", f.UID, f.Overlaps)
		}
	}
	if !fx[2].PastEnd || fx[0].PastEnd {
		t.Errorf("past end: %+v", fx)
	}
}

func TestRDMEditFixture(t *testing.T) {
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)

	for body, want := range map[string]string{
		`{}`:                "nothing to change",
		`{"start":0}`:       "not 1-512",
		`{"start":513}`:     "not 1-512",
		`{"personality":0}`: "not 1-255",
		`{"label":"Café"}`:  "plain ASCII",
		`{"label":"` + strings.Repeat("x", 33) + `"}`: "at most 32",
	} {
		if code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC114", body); code != 400 || !strings.Contains(out["error"].(string), want) {
			t.Errorf("%s: %d %v", body, code, out)
		}
	}

	// Personality first (16 ch), then address, label and identify, then read back.
	code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75%3A000DC114", `{"personality":1,"start":101,"label":"SL 3","identify":true}`)
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	f := out["fixture"].(map[string]any)
	if f["start"] != float64(101) || f["personality"] != float64(1) || f["footprint"] != float64(10) || f["label"] != "SL 3" || f["identify"] != true {
		t.Errorf("fixture after edit %v", f)
	}

	// A refusal part way says what already went.
	n.fake.Lock()
	n.fake.Ports[1][0].ReadOnlyLabel = true // fixture ...C114
	n.fake.Unlock()
	code, out = rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC114", `{"start":200,"label":"x"}`)
	if code != 502 || !strings.Contains(out["error"].(string), "write protected") || !strings.Contains(out["error"].(string), "already set: address 200") {
		t.Errorf("label refused: %d %v", code, out)
	}

	// The personality list.
	code, out = rdmCall(t, srv, "GET", "/rdm/1/fixture/4F75:000DC114/personalities", "")
	ps, _ := out["personalities"].([]any)
	if code != 200 || len(ps) != 2 || ps[1].(map[string]any)["name"] != "HSI" || ps[1].(map[string]any)["footprint"] != float64(16) {
		t.Errorf("personalities %d %v", code, out)
	}
	if code, out = rdmCall(t, srv, "POST", "/rdm/2/fixture/4F75:000DC114", `{"identify":true}`); code != 502 || !strings.Contains(out["error"].(string), "RDM is off") {
		t.Errorf("edit on a port with RDM off: %d %v", code, out)
	}
}

func TestRDMEditIsNotSentUnderDryRun(t *testing.T) {
	setDry(t, true)
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC113", `{"start":300,"identify":true}`)
	if code != 200 || out["dryRun"] != true {
		t.Fatalf("%d %v", code, out)
	}
	n.fake.Lock()
	defer n.fake.Unlock()
	if fx := n.fake.Ports[1][1]; fx.Start != 1 || fx.Identify {
		t.Errorf("the fixture changed under DRY_RUN: %+v", fx)
	}
}

func TestRDMFixturePage(t *testing.T) {
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	n.fake.Lock()
	vx := n.fake.Ports[1][1].WithVortexParams() // ...C113
	n.fake.Unlock()

	code, out := rdmCall(t, srv, "GET", "/rdm/1/fixture/4F75:000DC113/details", "")
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if out["hours"] != float64(1605) || out["lampHours"] != float64(887) || out["canFactoryReset"] != true || out["canRestart"] != true || out["atDefaults"] != false {
		t.Errorf("details %v", out)
	}
	sensors := out["sensors"].([]any)
	if len(sensors) != 5 || sensors[0].(map[string]any)["name"] != "LED Panel Temperature" || sensors[0].(map[string]any)["value"] != float64(14) || sensors[0].(map[string]any)["unit"] != "°C" {
		t.Errorf("sensors %v", sensors)
	}
	byName := map[string]map[string]any{}
	for _, s := range out["settings"].([]any) {
		byName[s.(map[string]any)["name"].(string)] = s.(map[string]any)
	}
	if _, ok := byName["DMX Smoothing Name"]; ok {
		t.Error("the Name parameter should be folded into DMX Smoothing")
	}
	checks := []struct {
		name, kind string
		value      any
		text       string
		readOnly   bool
	}{
		{"Dimmer curve", "choice", float64(1), "", false},
		{"DMX Smoothing", "number", float64(1), "Off", false},
		{"RGBW Colourspace", "number", float64(1), "Calibrated RGBW", false},
		{"CRMX Enabled", "bool", float64(1), "", false},
		{"CRMX Link State", "bool", float64(0), "", true},
		{"SACN Universe", "number", float64(1), "", false},
		{"Restore DMX Preset", "action", nil, "", false},
		{"Device Location", "text", nil, "", false},
	}
	for _, c := range checks {
		s := byName[c.name]
		if s == nil || s["kind"] != c.kind || s["value"] != c.value || (c.text != "" && s["text"] != c.text) || s["readOnly"] != c.readOnly {
			t.Errorf("%s: %v", c.name, s)
		}
	}
	if ch := byName["Dimmer curve"]["choices"].([]any); len(ch) != 4 || ch[1] != "Exponential" {
		t.Errorf("curve choices %v", ch)
	}

	// Set a few, each read back.
	for _, c := range []struct {
		body string
		want any
	}{
		{`{"pid":34304,"value":false}`, float64(0)}, // 0x8600 CRMX Enabled off
		{`{"pid":34561,"value":512}`, float64(512)}, // 0x8701 SACN Universe
		{`{"pid":835,"value":2}`, float64(2)},       // 0x0343 curve
		{`{"pid":33792,"value":"Truss 2 SL"}`, nil}, // 0x8400 location
	} {
		code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC113/param", c.body)
		if code != 200 || out["setting"].(map[string]any)["value"] != c.want {
			t.Errorf("%s: %d %v", c.body, code, out)
		}
	}
	if code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC113/param", `{"pid":34561,"value":70000}`); code != 502 || !strings.Contains(out["error"].(string), "1-63999") {
		t.Errorf("out of range: %d %v", code, out)
	}
	if code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC113/param", `{"pid":34305,"value":1}`); code != 502 || !strings.Contains(out["error"].(string), "read-only") {
		t.Errorf("read-only: %d %v", code, out)
	}
	n.fake.Lock()
	if vx.Params[9].Value[0] != 0 || vx.Params[13].Value[1] != 0 || vx.Params[13].Value[0] != 2 || vx.Curve != 2 || string(vx.Params[15].Value) != "Truss 2 SL" {
		t.Errorf("fixture now: crmx %v universe %v curve %d location %q", vx.Params[9].Value, vx.Params[13].Value, vx.Curve, vx.Params[15].Value)
	}
	vx.Start = 300
	n.fake.Unlock()

	// Factory reset: defaults back, read back.
	code, out = rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC113/factory-reset", "")
	if code != 200 || out["fixture"].(map[string]any)["start"] != float64(1) {
		t.Errorf("factory reset: %d %v", code, out)
	}
	code, out = rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC113/restart", "")
	n.fake.Lock()
	// The fixture's own factory values come back (CRMX on), not the descriptions' defaults (0).
	if code != 200 || vx.Restarts != 1 || !vx.AtDefaults || vx.Params[9].Value[0] != 1 || vx.Curve != 1 || string(vx.Params[15].Value) != "" {
		t.Errorf("restart %d %v; fixture restarts %d defaults %v crmx %v curve %d", code, out, vx.Restarts, vx.AtDefaults, vx.Params[9].Value, vx.Curve)
	}
	n.fake.Unlock()
}

func TestRDMFixturePageDryRun(t *testing.T) {
	setDry(t, true)
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	n.fake.Lock()
	vx := n.fake.Ports[1][1].WithVortexParams()
	n.fake.Unlock()
	for _, path := range []string{"/param", "/factory-reset", "/restart"} {
		body := ""
		if path == "/param" {
			body = `{"pid":34304,"value":0}`
		}
		if code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC113"+path, body); code != 200 || out["dryRun"] != true {
			t.Errorf("%s: %d %v", path, code, out)
		}
	}
	n.fake.Lock()
	defer n.fake.Unlock()
	if vx.Params[9].Value[0] != 1 || vx.Restarts != 0 || vx.AtDefaults {
		t.Errorf("the fixture changed under DRY_RUN: crmx %v restarts %d defaults %v", vx.Params[9].Value, vx.Restarts, vx.AtDefaults)
	}
}

func TestRDMReadIncludesWhatTheNodeListsAndFoundCount(t *testing.T) {
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	n.extra = []string{"4F:75:00:0D:C1:FF"}
	if code, out := rdmCall(t, srv, "GET", "/rdm/1/found", ""); code != 200 || out["count"] != float64(3) {
		t.Errorf("found: %d %v", code, out)
	} else if u, _ := out["uids"].([]any); len(u) != 3 || !(u[0].(string) < u[1].(string) && u[1].(string) < u[2].(string)) {
		t.Errorf("found uids, sorted: %v", out["uids"])
	}
	code, out := rdmCall(t, srv, "GET", "/rdm/1/fixtures", "")
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	fx := out["fixtures"].([]any)
	var extra map[string]any
	for _, f := range fx {
		if f.(map[string]any)["uid"] == "4F75:000DC1FF" {
			extra = f.(map[string]any)
		}
	}
	if len(fx) != 3 || extra == nil || !strings.Contains(extra["error"].(string), "no RDM reply") {
		t.Errorf("fixtures %v", fx)
	}
}

// Two ports on one universe: each read shows only its own port's fixtures,
// whatever BindIndex the node puts on the tables (LumiNode 12 - 4, U33, 16 Nov
// 2026: port 1's table came as 2 and port 2's as 255, and a read of port 2 showed
// both ports' fixtures).
func TestRDMReadOfAPortSharingItsUniverse(t *testing.T) {
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	n.ios[2]["io_type"], n.ios[2]["rdm"], n.ios[2]["rdm_universe"] = "output", true, float64(1)
	c := &artnet.FakeFixture{UID: 0x4F75000DC201, Model: "Vortex 8S", Category: 0x0101, Start: 1, Personality: 1, Personalities: []artnet.FakePersonality{{Footprint: 10}}}
	d := &artnet.FakeFixture{UID: 0x4F75000DC202, Model: "Vortex 8S", Category: 0x0101, Start: 11, Personality: 1, Personalities: []artnet.FakePersonality{{Footprint: 10}}}
	n.port2 = []string{"4F:75:00:0D:C2:01", "4F:75:00:0D:C2:02"}
	n.fake.Lock()
	n.fake.SharedPorts = map[artnet.PortAddress][][]*artnet.FakeFixture{1: {{c, d}}}
	n.fake.TodBinds = map[artnet.PortAddress][]int{1: {255, 2}} // port 2's table, then port 1's
	n.fake.Unlock()
	uids := func(port string) []string {
		code, out := rdmCall(t, srv, "GET", "/rdm/"+port+"/fixtures", "")
		if code != 200 {
			t.Fatalf("port %s: %d %v", port, code, out)
		}
		var got []string
		for _, f := range out["fixtures"].([]any) {
			got = append(got, f.(map[string]any)["uid"].(string))
		}
		return got
	}
	if got := strings.Join(uids("2"), " "); got != "4F75:000DC201 4F75:000DC202" {
		t.Errorf("port 2 = %s", got)
	}
	if got := strings.Join(uids("1"), " "); got != "4F75:000DC113 4F75:000DC114" {
		t.Errorf("port 1 = %s", got)
	}
	// Port 2 with nothing on its line sends no table: port 1's is not taken for it.
	n.mu.Lock()
	n.port2 = nil
	n.mu.Unlock()
	n.fake.Lock()
	n.fake.SharedPorts, n.fake.TodBinds = nil, map[artnet.PortAddress][]int{1: {2}}
	n.fake.Unlock()
	if got := uids("2"); len(got) != 0 {
		t.Errorf("empty port 2 = %v", got)
	}
}

// LumiNode 12 - 5, ports 9 and 10 on U19 (19 Nov 2026): both tables came with
// BindIndex 255, 1.7.0 merged them, port 10 showed port 9's fixtures too, and an
// address meant for port 10 went to a fixture on port 9. Each port now shows only
// what the node lists on it, and a SET from the wrong port is refused unsent.
func TestRDMSharedUniverseWithTheSameBindIndex(t *testing.T) {
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	n.ios[2]["io_type"], n.ios[2]["rdm"], n.ios[2]["rdm_universe"] = "output", true, float64(1)
	c := &artnet.FakeFixture{UID: 0x4F75000DC201, Model: "Vortex 8S", Category: 0x0101, Start: 1, Personality: 1, Personalities: []artnet.FakePersonality{{Footprint: 10}}}
	stray := &artnet.FakeFixture{UID: 0x4F75000DC2FF, Model: "Vortex 8S", Category: 0x0101, Start: 41, Personality: 1, Personalities: []artnet.FakePersonality{{Footprint: 10}}}
	n.port2 = []string{"4F:75:00:0D:C2:01"}
	n.fake.Lock()
	n.fake.SharedPorts = map[artnet.PortAddress][][]*artnet.FakeFixture{1: {{c, stray}}} // stray: in the table, on no list
	n.fake.TodBinds = map[artnet.PortAddress][]int{1: {255, 255}}
	n.fake.Unlock()
	read := func(port string) ([]string, string) {
		code, out := rdmCall(t, srv, "GET", "/rdm/"+port+"/fixtures", "")
		if code != 200 {
			t.Fatalf("port %s: %d %v", port, code, out)
		}
		var got []string
		for _, f := range out["fixtures"].([]any) {
			got = append(got, f.(map[string]any)["uid"].(string))
		}
		note, _ := out["note"].(string)
		return got, note
	}
	got, note := read("2")
	if strings.Join(got, " ") != "4F75:000DC201" {
		t.Errorf("port 2 = %v", got)
	}
	if !strings.Contains(note, "1 more fixture") {
		t.Errorf("port 2 note = %q", note)
	}
	if got, _ := read("1"); strings.Join(got, " ") != "4F75:000DC113 4F75:000DC114" {
		t.Errorf("port 1 = %v", got)
	}

	n.fake.Lock()
	before := n.fake.Requests
	n.fake.Unlock()
	for _, uid := range []string{"4F75:000DC113", "4F75:000DC2FF"} { // port 1's, and on no list
		code, out := rdmCall(t, srv, "POST", "/rdm/2/fixture/"+uid, `{"start":161}`)
		if code == 200 || !strings.Contains(out["error"].(string), "nothing sent") {
			t.Errorf("address %s from port 2: %d %v", uid, code, out)
		}
		code, out = rdmCall(t, srv, "POST", "/rdm/2/fixture/"+uid, `{"identify":true}`)
		if code == 200 {
			t.Errorf("identify %s from port 2: %d %v", uid, code, out)
		}
	}
	n.fake.Lock()
	sent := n.fake.Requests - before
	n.fake.Unlock()
	if sent != 0 {
		t.Errorf("%d RDM requests went out for refused SETs", sent)
	}
	// Port 2's own fixture still takes it.
	if code, out := rdmCall(t, srv, "POST", "/rdm/2/fixture/4F75:000DC201", `{"start":161}`); code != 200 {
		t.Errorf("address port 2's own fixture: %d %v", code, out)
	}
	n.fake.Lock()
	defer n.fake.Unlock()
	if c.Start != 161 {
		t.Errorf("port 2's fixture at %d", c.Start)
	}
}

func TestRDMDiscoverySendsAJSONBody(t *testing.T) {
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	if code, out := rdmCall(t, srv, "POST", "/rdm/1/discovery", ""); code != 200 {
		t.Errorf("discovery against a node that wants JSON: %d %v", code, out)
	}
}

func TestRDMIdentifyAloneReadsBackOnlyIdentify(t *testing.T) {
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	n.fake.Lock()
	before := n.fake.Requests
	n.fake.Unlock()
	code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC113", `{"identify":true}`)
	f, _ := out["fixture"].(map[string]any)
	if code != 200 || f["identify"] != true || f["partial"] != true || f["uid"] != "4F75:000DC113" {
		t.Fatalf("%d %v", code, out)
	}
	n.fake.Lock()
	sent := n.fake.Requests - before
	on := n.fake.Ports[1][1].Identify
	n.fake.Unlock()
	if sent != 2 || !on { // the SET and one GET of IDENTIFY_DEVICE
		t.Errorf("%d RDM requests, identify %v; want 2, true", sent, on)
	}
}

// Factory reset keeping the address: the fixture's own address goes back once
// it answers again; the personality stays as the reset left it (Nick, 17 Nov 2026).
func TestRDMFactoryResetKeepsAddress(t *testing.T) {
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	n.fake.Lock()
	b := n.fake.Ports[1][0] // DC114, personality 2 of 2
	b.Start, b.Label = 300, "keep me?"
	n.fake.Unlock()

	code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC114/factory-reset", `{"keepAddress":true}`)
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	f := out["fixture"].(map[string]any)
	if f["start"] != float64(300) || f["personality"] != float64(1) || f["label"] != "" || out["note"] != "address 300 kept" {
		t.Errorf("after reset: %v note %v", f, out["note"])
	}
	n.fake.Lock()
	if b.Start != 300 || b.Personality != 1 || b.Label != "" {
		t.Errorf("fixture now start %d personality %d label %q", b.Start, b.Personality, b.Label)
	}
	b.Start = 300
	n.fake.Unlock()

	// Without it the address goes to the factory one, as before.
	if code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC114/factory-reset", ""); code != 200 || out["fixture"].(map[string]any)["start"] != float64(1) || out["note"] != nil {
		t.Errorf("plain reset: %d %v", code, out)
	}

	// A fixture whose address cannot be read is not reset at all.
	n.fake.Lock()
	b.Start, b.Personality, b.Silent = 300, 2, true
	n.fake.Unlock()
	if code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC114/factory-reset", `{"keepAddress":true}`); code == 200 || !strings.Contains(out["error"].(string), "nothing was sent") {
		t.Errorf("silent fixture: %d %v", code, out)
	}
	n.fake.Lock()
	if b.Personality != 2 {
		t.Errorf("silent fixture was reset")
	}
	n.fake.Unlock()
}

func TestRDMFactoryResetKeepsAddressDryRun(t *testing.T) {
	setDry(t, true)
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	n.fake.Lock()
	b := n.fake.Ports[1][0]
	b.Start = 300
	n.fake.Unlock()
	code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC114/factory-reset", `{"keepAddress":true}`)
	if code != 200 || out["dryRun"] != true || out["note"] != "would keep address 300" {
		t.Errorf("%d %v", code, out)
	}
	n.fake.Lock()
	defer n.fake.Unlock()
	if b.Start != 300 || b.Personality != 2 || b.AtDefaults {
		t.Errorf("the fixture changed under DRY_RUN: start %d personality %d", b.Start, b.Personality)
	}
}

// The fixture reset but its acknowledgement never came: the address still goes back.
func TestRDMFactoryResetKeepsAddressWhenTheResetIsNotAnswered(t *testing.T) {
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	n.fake.Lock()
	b := n.fake.Ports[1][0]
	b.Start, b.MuteReset = 300, true
	n.fake.Unlock()
	code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC114/factory-reset", `{"keepAddress":true}`)
	n.fake.Lock()
	defer n.fake.Unlock()
	if code != 200 || out["note"] != "address 300 kept" || b.Start != 300 || !b.AtDefaults && b.Personality != 1 {
		t.Errorf("%d %v; fixture start %d personality %d", code, out, b.Start, b.Personality)
	}
}

// A fixture that acknowledges first and applies its defaults a moment later must not be called "kept" on the
// strength of the first read.
func TestRDMFactoryResetKeepsAddressOfALateResetter(t *testing.T) {
	old := resetSettle
	resetSettle = 300 * time.Millisecond
	t.Cleanup(func() { resetSettle = old })
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	n.fake.Lock()
	b := n.fake.Ports[1][0]
	b.Start, b.LateReset = 300, true
	n.fake.Unlock()
	code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC114/factory-reset", `{"keepAddress":true}`)
	n.fake.Lock()
	defer n.fake.Unlock()
	if code != 200 || out["note"] != "address 300 kept" || b.Start != 300 || b.Personality != 1 {
		t.Errorf("%d %v; fixture start %d personality %d", code, out, b.Start, b.Personality)
	}
}
