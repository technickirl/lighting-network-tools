package sacn

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cid(b byte) CID {
	var c CID
	for i := range c {
		c[i] = b
	}
	return c
}

func TestDataPacketLayout(t *testing.T) {
	d := Data{CID: cid(0xab), Source: "Desk", Priority: 100, Seq: 7, Universe: 258, Slots: []byte{255, 0, 128}}
	b := d.Marshal()
	// The fixed points of an E1.31 data packet (ANSI E1.31-2018, tables 4-1, 6-1 and 7-1).
	for _, c := range []struct {
		at   int
		want []byte
		what string
	}{
		{0, []byte{0x00, 0x10, 0x00, 0x00}, "preamble and postamble size"},
		{4, []byte("ASC-E1.17\x00\x00\x00"), "ACN packet identifier"},
		{16, []byte{0x70, 113}, "root flags and length"},
		{18, []byte{0, 0, 0, 4}, "root vector"},
		{38, []byte{0x70, 91}, "framing flags and length"},
		{40, []byte{0, 0, 0, 2}, "framing vector"},
		{44, []byte("Desk\x00"), "source name"},
		{108, []byte{100, 0, 0, 7, 0, 1, 2}, "priority, sync, sequence, options, universe"},
		{115, []byte{0x70, 14, 0x02, 0xa1, 0, 0, 0, 1, 0, 4, 0}, "DMP layer and start code"},
		{126, []byte{255, 0, 128}, "slots"},
	} {
		if got := b[c.at : c.at+len(c.want)]; string(got) != string(c.want) {
			t.Errorf("%s at %d: % x, want % x", c.what, c.at, got, c.want)
		}
	}
	p, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	got := p.(*Data)
	if got.Source != "Desk" || got.Universe != 258 || got.Priority != 100 || got.Seq != 7 || string(got.Slots) != string(d.Slots) || got.CID != d.CID {
		t.Errorf("round trip: %+v", got)
	}
}

func TestDiscoveryRoundTrip(t *testing.T) {
	b := Discovery{CID: cid(1), Source: "Desk", Page: 0, Last: 0, Universes: []uint16{1, 2, 300}}.Marshal()
	p, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := p.(*Discovery)
	if !ok || d.Source != "Desk" || len(d.Universes) != 3 || d.Universes[2] != 300 {
		t.Errorf("got %+v", p)
	}
}

func TestParseRefusesBadPackets(t *testing.T) {
	good := Data{CID: cid(1), Universe: 1, Slots: make([]byte, 512)}.Marshal()
	short := append([]byte(nil), good[:200]...) // says 513 values, holds fewer
	notACN := append([]byte(nil), good...)
	notACN[4] = 'X'
	for name, b := range map[string][]byte{"empty": nil, "art-net": []byte("Art-Net\x00\x00\x50"), "cut short": short, "wrong identifier": notACN, "header only": good[:100]} {
		if p, err := Parse(b); err == nil {
			t.Errorf("%s: parsed as %+v", name, p)
		}
	}
}

func TestGroupAddr(t *testing.T) {
	if got := GroupAddr(258).String(); got != "239.255.1.2" {
		t.Errorf("universe 258: %s", got)
	}
	if got := GroupAddr(DiscoveryUniverse).String(); got != "239.255.250.214" {
		t.Errorf("discovery: %s", got)
	}
}

func TestUniverseLists(t *testing.T) {
	us, err := ParseUniverses(" 3, 1-2 5;7-8 2 ")
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatUniverses(us); got != "1-3, 5, 7-8" {
		t.Errorf("got %q", got)
	}
	if us, err := ParseUniverses(""); err != nil || len(us) != 0 {
		t.Errorf("empty list: %v %v", us, err)
	}
	for _, bad := range []string{"0", "64000", "5-1", "x", "1-", "1-200"} {
		if _, err := ParseUniverses(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// A receiver with no socket and a clock the test moves.
func testReceiver() (*Receiver, *time.Time) {
	r := newReceiver()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	return r, &now
}

func levelsOf(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 512 {
		t.Fatalf("levels: %d octets, %v", len(b), err)
	}
	return b
}

func TestMergeByPriorityThenHighest(t *testing.T) {
	r, now := testReceiver()
	ip := net.IPv4(10, 0, 0, 1)
	r.handle(Data{CID: cid(1), Source: "Desk", Priority: 100, Universe: 5, Slots: []byte{10, 200, 0}}.Marshal(), ip)
	r.handle(Data{CID: cid(2), Source: "Backup", Priority: 100, Universe: 5, Slots: []byte{50, 100, 0, 9}}.Marshal(), ip)
	r.handle(Data{CID: cid(3), Source: "Low", Priority: 50, Universe: 5, Slots: []byte{255, 255, 255}}.Marshal(), ip)
	r.handle(Data{CID: cid(4), Source: "Viz", Priority: 200, Options: OptPreview, Universe: 5, Slots: []byte{255, 255, 255}}.Marshal(), ip)

	u := r.Universe(5)
	if got := levelsOf(t, u.Merged)[:4]; string(got) != string([]byte{50, 200, 0, 9}) {
		t.Errorf("merged %v", got)
	}
	win := map[string]bool{}
	for _, s := range u.Sources {
		win[s.Name] = s.Winning
	}
	if !win["Desk"] || !win["Backup"] || win["Low"] || win["Viz"] {
		t.Errorf("winning: %v", win)
	}
	if u.Sources[0].Name != "Viz" || !u.Sources[0].Preview {
		t.Errorf("sources are listed by priority: %+v", u.Sources[0])
	}

	// The two at 100 go quiet: the one at 50 takes over, and the lost ones stay listed.
	*now = now.Add(2 * time.Second)
	r.handle(Data{CID: cid(3), Source: "Low", Priority: 50, Seq: 1, Universe: 5, Slots: []byte{1, 2, 3}}.Marshal(), ip)
	*now = now.Add(time.Second)
	u = r.Universe(5)
	if got := levelsOf(t, u.Merged)[:3]; string(got) != string([]byte{1, 2, 3}) {
		t.Errorf("after the loss: merged %v", got)
	}
	if len(u.Sources) != 4 {
		t.Errorf("%d sources listed, want the lost ones kept", len(u.Sources))
	}
	// Long gone: forgotten, and the universe with them.
	*now = now.Add(time.Minute)
	if u = r.Universe(5); len(u.Sources) != 0 {
		t.Errorf("sources not forgotten: %+v", u.Sources)
	}
	if st := r.Status(); len(st.Universes) != 0 {
		t.Errorf("universe still listed: %+v", st.Universes)
	}
}

func TestSequenceCounting(t *testing.T) {
	r, _ := testReceiver()
	ip := net.IPv4(10, 0, 0, 1)
	send := func(seq, level byte) {
		r.handle(Data{CID: cid(1), Priority: 100, Seq: seq, Universe: 1, Slots: []byte{level}}.Marshal(), ip)
	}
	send(250, 1)
	send(251, 2)
	send(254, 3) // 252 and 253 never came
	send(253, 4) // late: dropped
	send(255, 5)
	send(0, 6) // wraps
	s := r.Universe(1).Sources[0]
	if s.Lost != 2 || s.OutOfOrder != 1 {
		t.Errorf("lost %d, late %d; want 2 and 1", s.Lost, s.OutOfOrder)
	}
	if got := levelsOf(t, s.Levels)[0]; got != 6 {
		t.Errorf("level %d, want the last in-order packet's", got)
	}
}

func TestTerminatedAndPerAddressPriority(t *testing.T) {
	r, _ := testReceiver()
	ip := net.IPv4(10, 0, 0, 1)
	r.handle(Data{CID: cid(1), Priority: 100, Seq: 1, Universe: 1, Slots: []byte{99}}.Marshal(), ip)
	r.handle(Data{CID: cid(1), Priority: 100, Seq: 2, Universe: 1, StartCode: 0xDD, Slots: []byte{200}}.Marshal(), ip)
	s := r.Universe(1).Sources[0]
	if !s.PerAddressPriority || levelsOf(t, s.Levels)[0] != 99 {
		t.Errorf("a priority packet must not change the levels: %+v", s)
	}
	r.handle(Data{CID: cid(1), Priority: 100, Seq: 3, Options: OptTerminated, Universe: 1, Slots: []byte{99}}.Marshal(), ip)
	u := r.Universe(1)
	if u.Sources[0].Live || !u.Sources[0].Terminated || levelsOf(t, u.Merged)[0] != 0 {
		t.Errorf("a source that said it stopped still counts: %+v", u.Sources[0])
	}
}

func TestDiscoveredUniversesAreWatched(t *testing.T) {
	r, now := testReceiver()
	r.SetWatch([]uint16{1, 2})
	r.handle(Discovery{CID: cid(9), Source: "Desk", Universes: []uint16{2, 40}}.Marshal(), net.IPv4(10, 0, 0, 9))
	st := r.Status()
	if len(st.Universes) != 3 || st.Universes[2].Universe != 40 || st.Universes[2].Typed || !st.Universes[0].Typed {
		t.Errorf("universes: %+v", st.Universes)
	}
	if len(st.Discovered) != 1 || st.Discovered[0].Universes != "2, 40" || st.Discovered[0].IP != "10.0.0.9" {
		t.Errorf("discovered: %+v", st.Discovered)
	}
	*now = now.Add(time.Minute)
	if st = r.Status(); len(st.Universes) != 2 || len(st.Discovered) != 0 {
		t.Errorf("a source that stopped announcing is still listed: %+v", st)
	}
}

// The real socket, with a packet sent straight to it (no multicast needed).
func TestOpenReceivesUnicast(t *testing.T) {
	r, err := Open(0)
	if err != nil {
		t.Skipf("no multicast socket here: %v", err)
	}
	defer r.Close()
	c, err := net.Dial("udp4", net.JoinHostPort("127.0.0.1", itoa(r.Port())))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for seq := byte(0); time.Now().Before(deadline); seq++ {
		_, _ = c.Write(Data{CID: cid(1), Source: "Test", Priority: 100, Seq: seq, Universe: 7, Slots: []byte{42}}.Marshal())
		time.Sleep(20 * time.Millisecond)
		if u := r.Universe(7); len(u.Sources) == 1 && levelsOf(t, u.Merged)[0] == 42 {
			return
		}
	}
	t.Fatal("the packet never arrived")
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestRoutes(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sacn.json")
	old := settingsPath
	settingsPath = func() string { return file }
	r, _ := testReceiver()
	SetSharedForTests(r)
	defer func() { settingsPath = old; SetSharedForTests(nil) }()
	r.handle(Data{CID: cid(1), Source: "<b>Desk</b>", Priority: 100, Universe: 3, Slots: []byte{7}}.Marshal(), net.IPv4(10, 0, 0, 1))

	mux := http.NewServeMux()
	Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	call := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var sb strings.Builder
		_ = res.Write(&sb)
		return res.StatusCode, sb.String()
	}
	for _, c := range []struct {
		method, path, body string
		code               int
		want               string
	}{
		{"GET", "/api/sacn", "", 200, `"universe":3`},
		{"GET", "/api/sacn/universe/3", "", 200, `"merged":"Bw`},
		{"GET", "/api/sacn/universe/0", "", 400, "Universes run from 1"},
		{"GET", "/api/sacn/universe/x", "", 400, "Universes run from 1"},
		{"POST", "/api/sacn/watch", `{"universes":"1-200"}`, 400, "more than 128"},
		{"POST", "/api/sacn/watch", `{"universes":"9, 1-2"}`, 200, `"watch":"1-2, 9"`},
		{"GET", "/api/sacn", "", 200, `"watch":"1-2, 9"`},
	} {
		code, body := call(c.method, c.path, c.body)
		if code != c.code || !strings.Contains(body, c.want) {
			t.Errorf("%s %s: %d, want %d with %q in\n%s", c.method, c.path, code, c.code, c.want, body)
		}
	}
	if got := loadSettings().Watch; got != "1-2, 9" {
		t.Errorf("saved list %q", got)
	}
}
