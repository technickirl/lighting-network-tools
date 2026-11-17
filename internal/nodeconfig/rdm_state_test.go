package nodeconfig

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// livePage is a page subscribed to the node on /api/live.
func livePage(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	conn, err := websocket.Dial("ws"+strings.TrimPrefix(base, "http")+"/api/live", "", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := websocket.JSON.Send(conn, map[string]string{"type": "subscribe", "ip": "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	return conn
}

// nextRDM waits for an RDM state message that satisfies ok, skipping the rest.
func nextRDM(t *testing.T, conn *websocket.Conn, what string, ok func(s map[string]any) bool) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var m map[string]any
		if err := websocket.JSON.Receive(conn, &m); err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if m["type"] != "rdm" {
			continue
		}
		if s, _ := m["rdm"].(map[string]any); s != nil && ok(s) {
			return s
		}
	}
}

func readFixturesOf(s map[string]any, port string) []any {
	read, _ := s["read"].(map[string]any)
	r, _ := read[port].(map[string]any)
	fx, _ := r["fixtures"].([]any)
	return fx
}

func TestRDMStateIsSharedBetweenPages(t *testing.T) {
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	laptop, ipad := livePage(t, srv.URL), livePage(t, srv.URL)
	time.Sleep(100 * time.Millisecond) // both subscribed

	// A read on one device shows on both, with "Reading…" while it runs.
	go func() {
		if res, err := http.Get(srv.URL + "/api/node/127.0.0.1/rdm/1/fixtures"); err == nil {
			res.Body.Close()
		}
	}()
	nextRDM(t, ipad, "Reading…", func(s map[string]any) bool { b, _ := s["busy"].(map[string]any); return b["1"] == "Reading…" })
	for name, c := range map[string]*websocket.Conn{"laptop": laptop, "ipad": ipad} {
		s := nextRDM(t, c, "the read", func(s map[string]any) bool { return len(readFixturesOf(s, "1")) == 2 })
		if b, _ := s["busy"].(map[string]any); len(b) != 0 {
			t.Errorf("%s: still busy %v", name, b)
		}
	}

	// An edit shows on the other device, re-sorted with its overlaps worked out again.
	if code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC114", `{"start":101}`); code != 200 {
		t.Fatalf("edit %d %v", code, out)
	}
	s := nextRDM(t, ipad, "the edit", func(s map[string]any) bool {
		fx := readFixturesOf(s, "1")
		return len(fx) == 2 && fx[1].(map[string]any)["start"] == float64(101)
	})
	if ov := readFixturesOf(s, "1")[0].(map[string]any)["overlaps"]; ov != nil {
		t.Errorf("overlaps after moving apart: %v", ov)
	}

	// Identify alone merges into the fixture as it was.
	if code, out := rdmCall(t, srv, "POST", "/rdm/1/fixture/4F75:000DC113", `{"identify":true}`); code != 200 {
		t.Fatalf("identify %d %v", code, out)
	}
	s = nextRDM(t, laptop, "identify", func(s map[string]any) bool {
		fx := readFixturesOf(s, "1")
		return len(fx) == 2 && fx[0].(map[string]any)["identify"] == true
	})
	if f := readFixturesOf(s, "1")[0].(map[string]any); f["label"] != "CSV-8S 901395" || f["start"] != float64(1) {
		t.Errorf("identify replaced the fixture: %v", f)
	}

	// A page opened later starts from it.
	_, out := rdmCall(t, srv, "GET", "/rdm", "")
	st, _ := out["state"].(map[string]any)
	if fx := readFixturesOf(st, "1"); len(fx) != 2 {
		t.Errorf("state on GET /rdm: %v", out["state"])
	}

	// RDM off drops the port's fixtures for everyone.
	if code, out := rdmCall(t, srv, "PUT", "/rdm/1", `{"on":false}`); code != 200 {
		t.Fatalf("RDM off %d %v", code, out)
	}
	nextRDM(t, ipad, "RDM off", func(s map[string]any) bool {
		ports, _ := s["ports"].([]any)
		return len(ports) > 0 && ports[0].(map[string]any)["rdm"] == false && readFixturesOf(s, "1") == nil
	})
}

// A port with no fixtures goes to the pages as [], not null: null broke every
// render of the RDM tab (a LumiNode 12 port with RDM on and nothing on the line,
// 13 Nov 2026).
func TestRDMStateEmptyPortIsAnEmptyList(t *testing.T) {
	rdmStore.Lock()
	rdmStore.m = map[string]*rdmNodeState{}
	rdmStore.Unlock()
	t.Cleanup(func() { rdmStore.Lock(); rdmStore.m = map[string]*rdmNodeState{}; rdmStore.Unlock() })
	rdmStoreRead("10.0.0.7", &rdmRead{Port: 1, Universe: 35, Source: "artnet", Fixtures: []rdmFixture{}})
	b, err := json.Marshal(rdmSnapshot("10.0.0.7"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"fixtures":[]`) {
		t.Errorf("snapshot %s", b)
	}
}

// portCount is a port's found count in a pushed state (-2 when the port is missing).
func portCount(s map[string]any, port float64) float64 {
	ports, _ := s["ports"].([]any)
	for _, p := range ports {
		if m, _ := p.(map[string]any); m["port"] == port {
			c, _ := m["count"].(float64)
			return c
		}
	}
	return -2
}

// Every page's port count follows the node's list: the count from when the ports
// were read went on showing through a search and read (Nick, 16 Nov 2026).
func TestRDMStateCountFollowsTheNode(t *testing.T) {
	n, srv := newRDMNode(t)
	n.ios[1]["rdm"], n.ios[1]["rdm_universe"] = true, float64(1)
	if code, out := rdmCall(t, srv, "GET", "/rdm", ""); code != 200 {
		t.Fatalf("ports %d %v", code, out)
	} else if st, _ := out["state"].(map[string]any); portCount(st, 1) != 2 {
		t.Fatalf("count at load: %v", out["state"])
	}
	ipad := livePage(t, srv.URL)
	time.Sleep(100 * time.Millisecond)

	// The node finds another fixture: the next search poll tells every page.
	n.mu.Lock()
	n.extra = []string{"4F:75:00:0D:C1:FF"}
	n.mu.Unlock()
	if code, out := rdmCall(t, srv, "GET", "/rdm/1/found", ""); code != 200 || out["count"] != float64(3) {
		t.Fatalf("found %d %v", code, out)
	}
	nextRDM(t, ipad, "count 3 after a search poll", func(s map[string]any) bool { return portCount(s, 1) == 3 })

	// And a read updates it too, without a search poll.
	n.mu.Lock()
	n.extra = nil
	n.mu.Unlock()
	if code, out := rdmCall(t, srv, "GET", "/rdm/1/fixtures", ""); code != 200 {
		t.Fatalf("read %d %v", code, out)
	}
	nextRDM(t, ipad, "count 2 after a read", func(s map[string]any) bool { return portCount(s, 1) == 2 && len(readFixturesOf(s, "1")) == 2 })
}
