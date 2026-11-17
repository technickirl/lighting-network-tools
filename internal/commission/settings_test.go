package commission

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeGen1 imitates the handful of GigaCore Gen1 /config pages the settings code uses.
type fakeGen1 struct {
	name, descr, id, led string
	saves                int
	posts                []string
	down                 int             // /stat/ports requests still to refuse: the switch is not answering
	dropAfter            map[string]bool // POST paths whose connection is dropped after the change is made, as Gen1 does
}

func drop(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if c, _, err := hj.Hijack(); err == nil {
			c.Close()
		}
	}
}

func (f *fakeGen1) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	switch {
	case r.Method == "GET" && r.URL.Path == "/stat/ports":
		if f.down > 0 {
			f.down--
			drop(w)
			return
		}
		fmt.Fprint(w, "1/0/0")
	case r.Method == "GET" && r.URL.Path == "/config/switchlegend":
		fmt.Fprintf(w, "%s,%s,%s,LUM7264427,3,2,D0:69:9E:AE:D1:86", url.PathEscape(f.name), url.PathEscape(f.descr), f.id)
	case r.Method == "GET" && r.URL.Path == "/config/lmx_led":
		fmt.Fprintf(w, "%s|", f.led)
	case r.Method == "POST":
		f.posts = append(f.posts, r.URL.Path+" "+r.PostForm.Encode())
		switch r.URL.Path {
		case "/config/switchlegend":
			f.name, f.descr, f.id = r.PostForm.Get("sw_name"), r.PostForm.Get("sw_descr"), r.PostForm.Get("sw_id")
		case "/config/lmx_led":
			f.led = r.PostForm.Get("brightness")
		case "/config/icfg_conf_save":
			f.saves++
		}
		if f.dropAfter[r.URL.Path] {
			drop(w)
		}
	default:
		http.NotFound(w, r)
	}
}

// redirect sends every device connection to the test server at url (the shared client always dials a
// device's own address), and puts the real dialer back afterwards.
func redirect(t *testing.T, url string) {
	t.Helper()
	addr := strings.TrimPrefix(url, "http://")
	luminex.SetDeviceDialForTests(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})
	t.Cleanup(func() { luminex.SetDeviceDialForTests((&net.Dialer{}).DialContext) })
}

func withFake(t *testing.T) (*fakeGen1, luminex.Device) {
	t.Helper()
	f := &fakeGen1{name: "GigaCore10", descr: "Luminex Gigacore 10 port", id: "1", led: "2", dropAfter: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	oldDry := luminex.DryRun
	luminex.DryRun = false
	redirect(t, srv.URL)
	t.Cleanup(func() { luminex.DryRun = oldDry })
	return f, luminex.Device{IP: "127.0.0.1", Family: "gigacore", APIKind: "gen1", Serial: "LUM7264427"}
}

// fakeNode imitates a LumiNode's /api/deviceinfo.
type fakeNode struct {
	info       map[string]any
	auth       bool     // web_auth_enabled
	authPuts   []string // bodies sent to PUT /api/deviceinfo/auth
	ignore     bool     // accepts the auth change but does not apply it
	identified int      // POST /api/identify calls
}

func (f *fakeNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/deviceinfo/auth":
		fmt.Fprintf(w, `{"web_auth_enabled":%v}`, f.auth)
	case r.Method == "PUT" && r.URL.Path == "/api/deviceinfo/auth":
		var in struct {
			On bool `json:"web_auth_enabled"`
		}
		b, _ := io.ReadAll(r.Body)
		f.authPuts = append(f.authPuts, string(b))
		_ = json.Unmarshal(b, &in)
		if !f.ignore {
			f.auth = in.On
		}
		w.WriteHeader(204)
	case r.Method == "GET" && r.URL.Path == "/api/deviceinfo":
		_ = json.NewEncoder(w).Encode(f.info)
	case r.Method == "POST" && r.URL.Path == "/api/identify":
		f.identified++
		w.WriteHeader(204)
	case r.Method == "PUT" && r.URL.Path == "/api/deviceinfo":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.info["short_name"] = in["short_name"]
		if v, ok := in["long_name"]; ok {
			f.info["long_name"] = v
		}
		if v, ok := in["colors"]; ok {
			f.info["colors"] = v
		}
		if v, ok := in["ID"]; ok {
			f.info["ID"] = v
		}
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}

func TestRenameNode(t *testing.T) {
	f := &fakeNode{info: map[string]any{"type": "LumiNode4", "serial": "LUM669433015", "ID": float64(7), "short_name": "LumiNode4", "long_name": "Front of house", "colors": []any{"#112233"}}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	oldDry := luminex.DryRun
	luminex.DryRun = false
	defer func() { luminex.DryRun = oldDry }()
	redirect(t, srv.URL)
	d := luminex.Device{IP: "127.0.0.1", Family: "luminode", APIKind: "luminode", Serial: "LUM669433015"}

	if s, msg := applySetting(d, "name", "Node 3.1", nil); s != "done" {
		t.Fatalf("rename: %s %q", s, msg)
	}
	if f.info["short_name"] != "Node 3.1" || f.info["long_name"] != "Node 3.1" || f.info["ID"] != float64(7) || len(f.info["colors"].([]any)) != 1 {
		t.Errorf("rename changed more than the names: %+v", f.info)
	}
	if s, _ := applySetting(d, "led", "3", nil); s != "failed" {
		t.Errorf("a LumiNode LED change should be refused, got %s", s)
	}
	if checkValue("name", "luminode", "12p Node 4.7") != "" || checkValue("name", "luminode", "A node name that is too long") == "" {
		t.Error("node name length check wrong")
	}
}

func TestApplyGen1(t *testing.T) {
	adm := &Auth{Username: "admin"}
	f, d := withFake(t)

	if s, msg := applySetting(d, "name", "Switch 2.3", adm); s != "done" || f.name != "Switch 2.3" || f.descr != "Luminex Gigacore 10 port" || f.id != "1" || f.saves != 1 {
		t.Errorf("name: %s %q, fake=%+v", s, msg, f)
	}
	if s, msg := applySetting(d, "led", "4", adm); s != "done" || f.led != "4" || f.saves != 2 {
		t.Errorf("led: %s %q, fake=%+v", s, msg, f)
	}
	if s, _ := applySetting(d, "identify", "", adm); s != "done" || f.posts[len(f.posts)-1] != "/config/lmx wink=1" {
		t.Errorf("identify: %s, posts=%v", s, f.posts)
	}
}

func TestDryRunSendsNothing(t *testing.T) {
	f, d := withFake(t)
	luminex.DryRun = true
	s, msg := applySetting(d, "led", "0", &Auth{Username: "admin"})
	if s != "dry_run" || !strings.Contains(msg, "Would send") || len(f.posts) != 0 || f.led != "2" {
		t.Errorf("dry run: %s %q, posts=%v led=%s", s, msg, f.posts, f.led)
	}
}

func TestReadBackCatchesAChangeThatDidNotTake(t *testing.T) {
	f, d := withFake(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			return // accepts the write but ignores it
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	redirect(t, srv.URL)
	if s, msg := applySetting(d, "led", "4", &Auth{Username: "admin"}); s != "failed" || !strings.Contains(msg, "could not confirm") {
		t.Errorf("expected a failed read-back, got %s %q", s, msg)
	}
}

func TestCheckValue(t *testing.T) {
	cases := []struct {
		action, kind, value string
		ok                  bool
	}{
		{"name", "gen1", "Switch 2.1", true}, {"name", "gen1", "12p Node 4.7", true},
		{"name", "gen1", "A name that is way too long", false}, {"name", "gen1", "", false}, {"name", "gen1", "a,b", false},
		{"name", "gen2", "Switch 2.1", false}, {"name", "gen2", "Switch 2-1", true},
		{"led", "gen1", "4", true}, {"led", "gen1", "5", false}, {"led", "gen1", "x", false},
		{"identify", "gen1", "", true},
	}
	for _, c := range cases {
		if got := checkValue(c.action, c.kind, c.value) == ""; got != c.ok {
			t.Errorf("checkValue(%s, %s, %q) ok=%v, want %v", c.action, c.kind, c.value, got, c.ok)
		}
	}
}

func TestGen1DroppedConnectionStillSavesAndConfirms(t *testing.T) {
	f, d := withFake(t)
	f.dropAfter["/config/lmx_led"] = true // the switch applies it, then drops the connection, as Gen1 does
	s, msg := applySetting(d, "led", "4", &Auth{Username: "admin"})
	if s != "done" || f.led != "4" || f.saves != 1 {
		t.Errorf("a dropped Gen1 config POST should still be saved and read back: %s %q, led=%s saves=%d", s, msg, f.led, f.saves)
	}
}

// Identify has no read-back, so after a dropped connection the switch must still be answering.
func TestGen1IdentifyDropChecksTheSwitchAnswers(t *testing.T) {
	f, d := withFake(t)
	f.dropAfter["/config/lmx"] = true
	if s, msg := applySetting(d, "identify", "", &Auth{Username: "admin"}); s != "done" {
		t.Errorf("a switch that dropped the connection but still answers: %s %q", s, msg)
	}
	f.down = 1
	if s, msg := applySetting(d, "identify", "", &Auth{Username: "admin"}); s != "failed" || !strings.Contains(msg, "not answering") {
		t.Errorf("a switch that has gone away should fail: %s %q", s, msg)
	}
}

func TestSettingsSnapshotUsesTheFieldNameThePageReads(t *testing.T) {
	b, err := json.Marshal((&SettingsRun{id: "x", action: "led"}).snapshot())
	if err != nil || !strings.Contains(string(b), `"dryRun"`) {
		t.Errorf("snapshot JSON must carry dryRun (settings.js reads r.dryRun): %s %v", b, err)
	}
}

func TestResetAndAddressJobsWaitForARunningSettingsRun(t *testing.T) {
	old := state.settings
	defer func() { state.settings = old }()
	state.settings = &SettingsRun{id: "x", action: "led", items: []*SettingsItem{{ID: "LUM1", Status: "sending", Device: luminex.Device{IP: "192.168.2.1"}}}}

	if locked, why := Locked("192.168.2.1"); !locked || why == "" {
		t.Errorf("a configure write to a device whose settings are being sent should be locked, got %v %q", locked, why)
	}
	if locked, _ := Locked("192.168.2.9"); locked {
		t.Error("an unrelated device should not be locked")
	}
	rec := httptest.NewRecorder()
	startJob(rec, nil)
	if rec.Code != 409 {
		t.Errorf("a reset or address job must be refused while settings are being sent, got HTTP %d", rec.Code)
	}
	state.settings.items[0].Status = "done"
	state.settings.done = true
	if locked, _ := Locked("192.168.2.1"); locked {
		t.Error("a finished settings run should not lock anything")
	}
}

func nodeFor(t *testing.T, f *fakeNode) luminex.Device {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	oldDry := luminex.DryRun
	luminex.DryRun = false
	t.Cleanup(func() { luminex.DryRun = oldDry })
	redirect(t, srv.URL)
	return luminex.Device{IP: "127.0.0.1", Family: "luminode", APIKind: "luminode", Serial: "LUM669433015"}
}

func TestTurnWebPasswordOffOnALumiNode(t *testing.T) {
	f := &fakeNode{info: map[string]any{"ID": float64(7), "short_name": "LumiNode4"}, auth: true}
	d := nodeFor(t, f)
	s, msg := applySetting(d, "webauth", "off", nil)
	if s != "done" || f.auth || len(f.authPuts) != 1 || f.authPuts[0] != `{"web_auth_enabled":false}` {
		t.Errorf("webauth: %s %q, auth=%v puts=%v", s, msg, f.auth, f.authPuts)
	}
	if f.info["short_name"] != "LumiNode4" {
		t.Errorf("turning the password off changed the device info: %+v", f.info)
	}
}

func TestWebPasswordReadBackCatchesAChangeThatDidNotTake(t *testing.T) {
	f := &fakeNode{info: map[string]any{}, auth: true, ignore: true}
	d := nodeFor(t, f)
	if s, msg := applySetting(d, "webauth", "off", nil); s != "failed" || !strings.Contains(msg, "still reports") {
		t.Errorf("expected a failed read-back, got %s %q", s, msg)
	}
}

func TestWebPasswordDryRunSendsNothing(t *testing.T) {
	f := &fakeNode{info: map[string]any{}, auth: true}
	d := nodeFor(t, f)
	luminex.DryRun = true
	s, msg := applySetting(d, "webauth", "off", nil)
	if s != "dry_run" || !strings.Contains(msg, "/api/deviceinfo/auth") || len(f.authPuts) != 0 || !f.auth {
		t.Errorf("dry run: %s %q, puts=%v auth=%v", s, msg, f.authPuts, f.auth)
	}
}

func TestWebPasswordOnIsNotOffered(t *testing.T) {
	if checkValue("webauth", "luminode", "on") == "" || checkValue("webauth", "gen2", "") == "" {
		t.Error("only off should be accepted")
	}
	if checkValue("webauth", "luminode", "off") != "" || checkValue("webauth", "gen2", "off") != "" {
		t.Error("off should be accepted")
	}
}

func TestGigaCoreGen1WebPasswordIsSkipped(t *testing.T) {
	f, d := withFake(t)
	run := &SettingsRun{id: "x", action: "webauth"}
	it := &SettingsItem{ID: "LUM7264427", Device: d, Value: "off"}
	run.items = []*SettingsItem{it}
	run.start()
	if !run.snapshot().Done {
		t.Error("a run whose only device was skipped should finish")
	}
	if it.Status != "skipped" || !strings.Contains(it.Detail, "Gen1") || len(f.posts) != 0 {
		t.Errorf("Gen1 should be skipped with a reason and nothing sent: %s %q posts=%v", it.Status, it.Detail, f.posts)
	}
	if s, _ := applySetting(d, "webauth", "off", &Auth{Username: "admin"}); s != "failed" || len(f.posts) != 0 {
		t.Errorf("applySetting on Gen1 should fail without sending: %s posts=%v", s, f.posts)
	}
}

// A GigaCore Gen2 switch, from the 1.5.0 spec: PUT /api/security/enabled with a JSON boolean.
func TestTurnWebPasswordOffOnGen2(t *testing.T) {
	enabled := true
	var put string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/security/enabled" && r.Method == "GET":
			fmt.Fprintf(w, "%v", enabled)
		case r.URL.Path == "/api/security/enabled" && r.Method == "PUT":
			b, _ := io.ReadAll(r.Body)
			put = string(b)
			enabled = string(b) == "true"
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := luminex.DryRun
	luminex.DryRun = false
	defer func() { luminex.DryRun = old }()
	redirect(t, srv.URL)
	d := luminex.Device{IP: "127.0.0.1", Family: "gigacore", APIKind: "gen2", Serial: "GC2"}
	if s, msg := applySetting(d, "webauth", "off", nil); s != "done" || put != "false" || enabled {
		t.Errorf("gen2 webauth: %s %q put=%q enabled=%v", s, msg, put, enabled)
	}
}

func TestIdentifyLumiNode(t *testing.T) {
	f := &fakeNode{info: map[string]any{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	oldDry := luminex.DryRun
	luminex.DryRun = false
	defer func() { luminex.DryRun = oldDry }()
	redirect(t, srv.URL)
	d := luminex.Device{IP: "127.0.0.1", Family: "luminode", APIKind: "luminode", Serial: "LUM669433015"}
	if s, msg := applySetting(d, "identify", "", nil); s != "done" || f.identified != 1 {
		t.Errorf("identify: %s %q, calls=%d", s, msg, f.identified)
	}
	luminex.DryRun = true
	if s, msg := applySetting(d, "identify", "", nil); s != "dry_run" || f.identified != 1 || !strings.Contains(msg, "/api/identify") {
		t.Errorf("dry run must send nothing: %s %q, calls=%d", s, msg, f.identified)
	}
}
