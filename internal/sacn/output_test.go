package sacn

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

// capture stands in for the network: every packet the sender would send.
type capture struct {
	mu   sync.Mutex
	pkts []any
	to   []uint16
}

func (c *capture) write(u uint16, pkt []byte) error {
	p, err := Parse(pkt)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pkts, c.to = append(c.pkts, p), append(c.to, u)
	return nil
}

func (c *capture) data() []*Data {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*Data
	for _, p := range c.pkts {
		if d, ok := p.(*Data); ok {
			out = append(out, d)
		}
	}
	return out
}

func TestSenderSendsThenSaysItStopped(t *testing.T) {
	s, c := NewSender(), &capture{}
	s.write = c.write
	if err := s.Set(7, map[int]byte{1: 255, 512: 9}); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(7, map[int]byte{513: 1}); err == nil {
		t.Error("address 513 was accepted")
	}
	if err := s.Start("test0", 201, ""); err == nil {
		t.Error("priority 201 was accepted")
	}
	if len(c.data()) != 0 {
		t.Fatal("something was sent before Start")
	}
	if err := s.Start("test0", 90, "Tester"); err != nil {
		t.Fatal(err)
	}
	if err := s.Start("test0", 90, "Tester"); err == nil {
		t.Error("a second Start was accepted")
	}
	time.Sleep(150 * time.Millisecond)
	s.Stop()
	s.Stop() // harmless

	ds := c.data()
	if len(ds) < 6 {
		t.Fatalf("%d data packets in 150 ms", len(ds))
	}
	for i, d := range ds {
		if d.Universe != 7 || d.Priority != 90 || d.Source != "Tester" || d.CID != s.CID() || len(d.Slots) != 512 || d.Slots[0] != 255 || d.Slots[511] != 9 {
			t.Fatalf("packet %d: %+v", i, d)
		}
		if i > 0 && d.Seq != ds[i-1].Seq+1 {
			t.Errorf("packet %d: sequence %d after %d", i, d.Seq, ds[i-1].Seq)
		}
		if last3 := i >= len(ds)-3; (d.Options&OptTerminated != 0) != last3 {
			t.Errorf("packet %d of %d: options %#x", i, len(ds), d.Options)
		}
	}
	c.mu.Lock()
	disc, ok := c.pkts[0].(*Discovery)
	to := c.to[0]
	n := len(c.pkts)
	c.mu.Unlock()
	if !ok || to != DiscoveryUniverse || len(disc.Universes) != 1 || disc.Universes[0] != 7 {
		t.Errorf("the first packet should announce universe 7: %+v to %d", c.pkts[0], to)
	}
	time.Sleep(60 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pkts) != n {
		t.Error("packets after Stop")
	}
}

func TestSenderDryRunSendsNothing(t *testing.T) {
	luminex.DryRun = true
	defer func() { luminex.DryRun = false }()
	s, c := NewSender(), &capture{}
	s.write = c.write
	_ = s.Set(1, map[int]byte{1: 255})
	if err := s.Start("test0", 100, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	s.Release(1)
	s.Stop()
	if len(c.pkts) != 0 {
		t.Errorf("%d packets sent under DRY_RUN", len(c.pkts))
	}
}

func TestSenderLimits(t *testing.T) {
	s := NewSender()
	for u := 1; u <= MaxOutput; u++ {
		if err := s.Ensure(uint16(u)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Ensure(500); err == nil {
		t.Errorf("universe %d was accepted", MaxOutput+1)
	}
	if err := s.Ensure(0); err == nil {
		t.Error("universe 0 was accepted")
	}
	s.Release(3)
	if err := s.Ensure(500); err != nil {
		t.Error(err)
	}
}

// A controller on temporary files, with no receiver socket and no network.
func testOutput(t *testing.T) (*output, *capture) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "sacn.json")
	oldPath, oldOut := settingsPath, out
	settingsPath = func() string { return file }
	rx, _ := testReceiver()
	SetSharedForTests(rx)
	out = &output{sender: NewSender()}
	c := &capture{}
	out.sender.write = c.write
	t.Cleanup(func() { out.sender.Stop(); settingsPath, out = oldPath, oldOut; SetSharedForTests(nil) })
	return out, c
}

func TestPatch(t *testing.T) {
	o, _ := testOutput(t)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.load()
	if err := o.add("Par 4", "rgb", 2, 10, 3); err != nil {
		t.Fatal(err)
	}
	for i, want := range []Fixture{{Name: "Par 4", Address: 10}, {Name: "Par 5", Address: 13}, {Name: "Par 6", Address: 16}} {
		if f := o.fixtures[i]; f.Name != want.Name || f.Address != want.Address || f.Universe != 2 || f.Profile != "RGB" {
			t.Errorf("fixture %d: %+v", i, f)
		}
	}
	for _, bad := range []struct {
		profile           string
		universe, addr, n int
		why               string
	}{
		{"RGB", 2, 18, 1, "is already patched"},
		{"RGB", 2, 511, 1, "past 512"},
		{"RGB", 2, 1, 4, "is already patched"},
		{"Nope", 2, 100, 1, "no profile"},
		{"RGB", 0, 1, 1, "universes run"},
		{"RGB", 2, 0, 1, "addresses run"},
	} {
		if err := o.add("x", bad.profile, bad.universe, bad.addr, bad.n); err == nil || !strings.Contains(err.Error(), bad.why) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if err := o.add("Wash", "Dimmer", 2, 1, 2); err != nil {
		t.Fatal(err)
	}
	if o.fixtures[0].Name != "Wash 1" || o.fixtures[1].Name != "Wash 2" || o.fixtures[2].Name != "Par 4" {
		t.Errorf("order and names: %+v", o.fixtures)
	}

	// A profile with a default: patched at it, back to it on Clear, zero when removed.
	o.profiles = append(o.profiles, Profile{Name: "Mover", Channels: []Channel{{Name: "Dim", Type: "dimmer"}, {Name: "Mode", Type: "other", Default: 128}}})
	if err := o.add("M", "Mover", 5, 101, 1); err != nil {
		t.Fatal(err)
	}
	_ = o.sender.Set(5, map[int]byte{101: 200, 102: 3, 300: 77})
	o.clear()
	if l, _ := o.sender.Levels(5); l[100] != 0 || l[101] != 128 || l[299] != 0 {
		t.Errorf("after Clear: %v %v %v", l[100], l[101], l[299])
	}
	var id int
	for _, f := range o.fixtures {
		if f.Name == "M" {
			id = f.ID
		}
	}
	o.remove([]int{id})
	if _, carried := o.sender.Levels(5); carried {
		t.Error("universe 5 is still in the output with nothing on it")
	}
	if _, carried := o.sender.Levels(2); !carried {
		t.Error("universe 2 was dropped though fixtures are patched there")
	}

	// The patch comes back on the next run, at the profiles' defaults.
	again := &output{sender: NewSender()}
	again.load()
	if len(again.fixtures) != 5 || again.fixtures[2].Name != "Par 4" {
		t.Errorf("reloaded patch: %+v", again.fixtures)
	}
}

func TestOutputRoutes(t *testing.T) {
	_, c := testOutput(t)
	mux := http.NewServeMux()
	Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	call := func(path, body string) (int, string) {
		t.Helper()
		var res *http.Response
		var err error
		if body == "" {
			res, err = http.Get(srv.URL + path)
		} else {
			res, err = http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		}
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var sb strings.Builder
		_ = res.Write(&sb)
		return res.StatusCode, sb.String()
	}
	for _, s := range []struct {
		path, body string
		code       int
		want       string
	}{
		{"/api/sacn/out", "", 200, `"running":false`},
		{"/api/sacn/out", "", 200, `"name":"Dimmer + RGBW"`},
		{"/api/sacn/out/start", `{"interface":"test0","priority":100}`, 400, "nothing to send yet"},
		{"/api/sacn/out/profiles/save", `{"name":"rgb","channels":[{"name":"R","type":"red"}]}`, 400, "built-in profile"},
		{"/api/sacn/out/profiles/save", `{"name":"Mine","channels":[{"name":"R","type":"pink"}]}`, 400, "not a channel type"},
		{"/api/sacn/out/profiles/save", `{"name":"Mine","channels":[]}`, 400, "1 to 512 channels"},
		{"/api/sacn/out/profiles/save", `{"name":" Mine ","channels":[{"name":"","type":"dimmer","default":255},{"name":"<b>","type":"red"}]}`, 200, `"name":"Mine","channels":[{"name":"Ch 1","type":"dimmer","default":255}`},
		{"/api/sacn/out/fixtures/add", `{"name":"A","profile":"Mine","universe":3,"address":511}`, 200, `"universe":3,"address":511`},
		{"/api/sacn/out/profiles/delete", `{"name":"mine"}`, 400, "uses this profile"},
		{"/api/sacn/out/levels", `{"universe":3,"set":[[512,256]]}`, 400, "levels run from 0 to 255"},
		{"/api/sacn/out/levels", `{"universe":3,"set":[[0,1]]}`, 400, "addresses run from 1 to 512"},
		{"/api/sacn/out/levels", `{"universe":3,"set":[[512,7]]}`, 200, `"ok":true`},
		{"/api/sacn/out/release", `{"universe":3}`, 400, "fixtures patched"},
		{"/api/sacn/out/start", `{"interface":"test0","priority":300}`, 400, "priority runs from 0 to 200"},
		{"/api/sacn/out/start", `{"interface":"test0","priority":120,"name":"Bench"}`, 200, `"running":true,"priority":120,"name":"Bench","interface":"test0"`},
		{"/api/sacn/out/start", `{"interface":"test0","priority":120}`, 400, "already sending"},
	} {
		code, body := call(s.path, s.body)
		if code != s.code || !strings.Contains(body, s.want) {
			t.Errorf("%s %s: %d, want %d with %q in\n%s", s.path, s.body, code, s.code, s.want, body)
		}
	}
	time.Sleep(80 * time.Millisecond)
	ds := c.data()
	if len(ds) == 0 || ds[0].Universe != 3 || ds[0].Priority != 120 || ds[0].Slots[510] != 255 || ds[0].Slots[511] != 7 {
		t.Fatalf("on the wire: %d packets, first %+v", len(ds), ds)
	}
	if code, body := call("/api/sacn/out/stop", "{}"); code != 200 || !strings.Contains(body, `"running":false`) {
		t.Errorf("stop: %d %s", code, body)
	}
	// The settings last started with are offered again; the list of universes to listen to is kept.
	var s settings
	b, _ := json.Marshal(loadSettings())
	_ = json.Unmarshal(b, &s)
	if s.Interface != "test0" || s.Priority == nil || *s.Priority != 120 || s.Name != "Bench" {
		t.Errorf("saved settings: %s", b)
	}
}

// The real sockets, only when asked for: SACN_LIVE_IFACE=en0 go test -run LiveLoopback ./internal/sacn
// It multicasts universe 63999 at priority 0, all zeros, from that interface for
// up to three seconds and expects this computer's own receiver to hear it. That
// does go onto the network, so it is never run by default.
func TestLiveLoopback(t *testing.T) {
	iface := os.Getenv("SACN_LIVE_IFACE")
	if iface == "" {
		t.Skip("set SACN_LIVE_IFACE to an interface name to run it")
	}
	rx, err := Open(Port)
	if err != nil {
		t.Fatal(err)
	}
	defer rx.Close()
	rx.SetWatch([]uint16{63999})
	s := NewSender()
	if err := s.Ensure(63999); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(iface, 0, "loopback test"); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		for _, src := range rx.Universe(63999).Sources {
			if src.CID == s.CID().String() && src.Live {
				t.Logf("heard from %s at %.0f Hz", src.IP, src.FPS)
				return
			}
		}
	}
	t.Fatalf("nothing heard on universe 63999 from %s: %s", iface, s.State().Error)
}
