package nodeconfig

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// oldNode answers like a LumiNode on firmware v2.6.3 (a real LumiNode 4, 27 Nov 2026): no /api/dmx/ports, and RDM
// discovery at /api/dmx/<port>/force_discovery. Its 404 body is the real one.
func oldNode(t *testing.T) (ip string, seen func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/api/dmx/ports" || strings.HasPrefix(r.URL.Path, "/api/dmx/ports/"):
			w.WriteHeader(404)
			w.Write([]byte(`{"name": "Not Found", "description": "The requested URL was not found on the server.", "code": 404}`))
		case r.URL.Path == "/api/deviceinfo":
			w.Write([]byte(`{"serial":"LUM071001210","short_name":"All output","nr_dmx_ports":4}`))
		case r.URL.Path == "/api/processblock" || r.URL.Path == "/api/IO" || r.URL.Path == "/api/profile":
			w.Write([]byte(`[]`))
		case r.URL.Path == "/api/ipsettings":
			w.Write([]byte(`{}`))
		case r.URL.Path == "/api/system/uptime":
			w.Write([]byte(`12`))
		default:
			w.WriteHeader(204)
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

// A node without /api/dmx/ports is still a node that answers: the page must not go to "unreachable" over it.
func TestNodeWithoutLivePortStateStillWorks(t *testing.T) {
	ip, seen := oldNode(t)
	t.Cleanup(func() { noLivePorts.Delete(ip) })

	snap, err := fetchSnapshot(ip)
	if err != nil || snap["noLivePorts"] != true || snap["dmxPorts"] != nil || snap["device"] == nil {
		t.Fatalf("snapshot: err %v, noLivePorts %v, dmxPorts %v", err, snap["noLivePorts"], snap["dmxPorts"])
	}
	before := len(seen())
	up, err := fetchLiveUpdate(ip)
	if err != nil {
		t.Fatalf("live update failed, so the page would show the node as lost: %v", err)
	}
	if _, has := up["dmxPorts"]; has {
		t.Error("live update carries port state the node never gave")
	}
	for _, c := range seen()[before:] {
		if c == "GET /api/dmx/ports" {
			t.Error("still asking for /api/dmx/ports every poll after the node said it has none")
		}
	}
}

// Without a snapshot first, the poller finds out by itself.
func TestLiveUpdateLearnsThereIsNoPortState(t *testing.T) {
	ip, _ := oldNode(t)
	t.Cleanup(func() { noLivePorts.Delete(ip) })
	if _, err := fetchLiveUpdate(ip); err != nil {
		t.Fatal(err)
	}
	if _, known := noLivePorts.Load(ip); !known {
		t.Error("not remembered")
	}
}

func TestNoSuchPathIsOnlyA404(t *testing.T) {
	if !noSuchPath(&statusError{Status: 404}) || noSuchPath(&statusError{Status: 500}) || noSuchPath(errNeedsPassword) || noSuchPath(nil) {
		t.Error("noSuchPath must be true for a 404 answer and nothing else")
	}
}
