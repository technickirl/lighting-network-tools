package nodeconfig

// The contract the routes rely on from lumiDo, checked against a local server.
// No test here talks to a real device.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type lumiSeen struct {
	Method, Path, Accept, ContentType, Body string
}

func lumiServer(t *testing.T, reply func(w http.ResponseWriter)) (ip string, seen *[]lumiSeen) {
	t.Helper()
	seen = &[]lumiSeen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*seen = append(*seen, lumiSeen{r.Method, r.URL.Path, r.Header.Get("Accept"), r.Header.Get("Content-Type"), string(b)})
		reply(w)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), seen
}

func TestLumiDoRequestShape(t *testing.T) {
	ip, seen := lumiServer(t, func(w http.ResponseWriter) { w.Write([]byte(`{"index":7}`)) })

	raw, err := lumiDo("GET", ip, "/api/IO", nil)
	if err != nil || string(raw) != `{"index":7}` {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
	if _, err = lumiDo("PUT", ip, "/api/IO/1", json.RawMessage(`{ "a" : 1 }`)); err != nil {
		t.Fatal(err)
	}
	if _, err = lumiDo("POST", ip, "/api/IO", obj{"universe": 1}); err != nil {
		t.Fatal(err)
	}
	want := []lumiSeen{
		{"GET", "/api/IO", "application/json", "", ""},
		{"PUT", "/api/IO/1", "application/json", "application/json", `{ "a" : 1 }`}, // forwarded verbatim
		{"POST", "/api/IO", "application/json", "application/json", `{"universe":1}`},
	}
	if len(*seen) != len(want) {
		t.Fatalf("requests = %+v", *seen)
	}
	for i := range want {
		if (*seen)[i] != want[i] {
			t.Errorf("request %d\n got %+v\nwant %+v", i, (*seen)[i], want[i])
		}
	}
}

func TestLumiDoAnswers(t *testing.T) {
	t.Run("empty reply is nil", func(t *testing.T) {
		ip, _ := lumiServer(t, func(w http.ResponseWriter) { w.WriteHeader(204) })
		if raw, err := lumiDo("POST", ip, "/api/identify", obj{}); raw != nil || err != nil {
			t.Errorf("raw=%s err=%v", raw, err)
		}
	})
	t.Run("error carries the status and the device's words", func(t *testing.T) {
		ip, _ := lumiServer(t, func(w http.ResponseWriter) { http.Error(w, "IO in use", 409) })
		_, err := lumiDo("DELETE", ip, "/api/IO/1", nil)
		if err == nil || err.Error() != "409 Conflict: IO in use" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("error with no text names the path", func(t *testing.T) {
		ip, _ := lumiServer(t, func(w http.ResponseWriter) { w.WriteHeader(404) })
		_, err := lumiDo("GET", ip, "/api/nope", nil)
		if err == nil || err.Error() != "404 Not Found (/api/nope)" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("no answer names the call", func(t *testing.T) {
		_, err := lumiDo("PUT", "127.0.0.1:1", "/api/IO/1", obj{})
		if err == nil || !strings.HasPrefix(err.Error(), "PUT /api/IO/1: ") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestLumiDoDryRun(t *testing.T) {
	setDry(t, true)
	ip, seen := lumiServer(t, func(w http.ResponseWriter) { w.Write([]byte(`[]`)) })

	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if raw, err := lumiDo(m, ip, "/api/processblock/0", obj{}); raw != nil || err != nil {
			t.Errorf("%s: raw=%s err=%v", m, raw, err)
		}
	}
	if len(*seen) != 0 {
		t.Fatalf("writes reached the device under DRY_RUN: %+v", *seen)
	}
	if raw, err := lumiDo("GET", ip, "/api/IO", nil); err != nil || string(raw) != "[]" {
		t.Errorf("read under DRY_RUN: raw=%s err=%v", raw, err)
	}
	if len(*seen) != 1 {
		t.Errorf("read did not reach the device: %+v", *seen)
	}
}

// An edit must reach the node as a PUT of the whole config: PATCH is refused
// without inputs and outputs, and a PUT resets every field it leaves out.
func TestEditBlockSendsTheWholeConfig(t *testing.T) {
	const current = `{"id":11,"name":"ProcessEngine 12","mode":"REROUTE","timeout":0,"backup_auto_recover":true,"ratelimiting":true,
		"colors":["#00ffa1"],"masterlimit":"NONE","startup_buffer":[1,2],"multi_channel_definitions":[],
		"inputs":{"0":11},"outputs":{"100011":100011},"status":{"x":1},"sources":{"inputs":[]}}`
	ip, seen := lumiServer(t, func(w http.ResponseWriter) { w.Write([]byte(current)) })

	if err := editBlock(ip, "11", obj{"name": "New name", "timeout": float64(0), "inputs": obj{}, "colors": []any{"#ff0000"}}); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 || (*seen)[0].Method != "GET" || (*seen)[1].Method != "PUT" || (*seen)[1].Path != "/api/processblock/11" {
		t.Fatalf("requests = %+v", *seen)
	}
	var sent obj
	if err := json.Unmarshal([]byte((*seen)[1].Body), &sent); err != nil {
		t.Fatal(err)
	}
	want := obj{"name": "New name", "mode": "REROUTE", "timeout": float64(0), "backup_auto_recover": true, "ratelimiting": true,
		"colors": []any{"#00ffa1"}, "masterlimit": "NONE", "startup_buffer": []any{float64(1), float64(2)}, "multi_channel_definitions": []any{}}
	got, _ := json.Marshal(sent)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Errorf("sent\n %s\nwant\n %s", got, exp)
	}
}

func TestEditBlockStopsWhenItCannotRead(t *testing.T) {
	ip, seen := lumiServer(t, func(w http.ResponseWriter) { http.Error(w, "busy", 500) })
	if err := editBlock(ip, "1", obj{"name": "x"}); err == nil {
		t.Fatal("no error")
	}
	if len(*seen) != 1 || (*seen)[0].Method != "GET" {
		t.Errorf("wrote without knowing the block: %+v", *seen)
	}
}

func TestEditBlockDryRunOnlyReads(t *testing.T) {
	setDry(t, true)
	ip, seen := lumiServer(t, func(w http.ResponseWriter) { w.Write([]byte(`{"mode":"HTP"}`)) })
	if err := editBlock(ip, "1", obj{"name": "x"}); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || (*seen)[0].Method != "GET" {
		t.Errorf("requests under DRY_RUN = %+v", *seen)
	}
}

// Same order as a job: nothing, then admin with a blank password, then ask.
func TestLumiDoTriesBlankPasswordThenAsks(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, has := r.BasicAuth()
		got = append(got, u+":"+p)
		if !has || p != "" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	ip := strings.TrimPrefix(srv.URL, "http://")
	if _, err := lumiDo("GET", ip, "/api/IO", nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != ":" || got[1] != "admin:" {
		t.Errorf("tried %v", got)
	}
	got = nil
	if _, err := lumiDo("GET", ip, "/api/IO", nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "admin:" {
		t.Errorf("should go straight to what worked, tried %v", got)
	}

	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer denied.Close()
	_, err := lumiDo("PUT", strings.TrimPrefix(denied.URL, "http://"), "/api/IO/1", obj{})
	if !errors.Is(err, errNeedsPassword) {
		t.Errorf("err = %v", err)
	}
}

// wiringNode is a stand-in node that keeps blocks and IOs, and like a real one deletes
// a network IO when its last use comes off and refuses an IO that is already attached.
type wiringNode struct {
	failCtrlPut bool // refuse the assign of a control IO
	blocks      []obj
	ios         []obj
	next        int
	calls       []string
}

// blockAt finds the block a /api/processblock/<n>/... path is about.
func (n *wiringNode) blockAt(path string) obj {
	i, _ := strconv.Atoi(strings.Split(path, "/")[3])
	return n.blocks[i]
}

func (n *wiringNode) attached(id int) bool {
	for _, b := range n.blocks {
		for _, side := range []string{"inputs", "outputs"} {
			for _, v := range b[side].(obj) {
				if int(v.(float64)) == id {
					return true
				}
			}
		}
	}
	return false
}

func (n *wiringNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		n.calls = append(n.calls, r.Method+" "+r.URL.Path)
	}
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	var body obj
	json.NewDecoder(r.Body).Decode(&body)
	block := n.blocks[0]
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/IO":
		reply(n.ios)
	case r.Method == "GET" && r.URL.Path == "/api/processblock":
		reply(n.blocks)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/processblock/"):
		reply(n.blocks[func() int { i, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/processblock/")); return i }()])
	case r.Method == "POST" && r.URL.Path == "/api/IO":
		body["id"] = float64(n.next)
		n.ios = append(n.ios, body)
		if body["io_type"] == "x-fade-ctrl" { // a real node attaches it to every block with the control unset
			for _, b := range n.blocks {
				if b["xfade_ctrl"] == float64(-1) {
					b["xfade_ctrl"] = float64(n.next)
				}
			}
		}
		reply(obj{"index": n.next})
		n.next++
	case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/api/processblock/0/input/"):
		if n.attached(int(body["io_id"].(float64))) {
			w.WriteHeader(500)
			return
		}
		block["inputs"].(obj)[strings.TrimPrefix(r.URL.Path, "/api/processblock/0/input/")] = body["io_id"]
		w.WriteHeader(204)
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/processblock/0/input/"):
		slot := strings.TrimPrefix(r.URL.Path, "/api/processblock/0/input/")
		id := int(block["inputs"].(obj)[slot].(float64))
		delete(block["inputs"].(obj), slot)
		if !n.attached(id) {
			for i, io := range n.ios {
				if int(io["id"].(float64)) == id {
					n.ios = append(n.ios[:i], n.ios[i+1:]...)
					break
				}
			}
		}
		w.WriteHeader(204)
	case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/x-fade-ctrl"):
		if n.failCtrlPut {
			w.WriteHeader(500)
			return
		}
		id := body["io_id"].(float64)
		for _, io := range n.ios {
			if io["id"] == id && io["io_type"] == "x-fade-ctrl" {
				n.blockAt(r.URL.Path)["xfade_ctrl"] = id
				w.WriteHeader(204)
				return
			}
		}
		w.WriteHeader(500) // a plain input IO is refused, as on a real node
	case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/x-fade-ctrl"):
		n.blockAt(r.URL.Path)["xfade_ctrl"] = float64(-1)
		w.WriteHeader(204)
	case r.Method == "PUT" && r.URL.Path == "/api/processblock/0/output":
		id := body["io_id"].(float64)
		if n.attached(int(id)) {
			w.WriteHeader(500)
			return
		}
		block["outputs"].(obj)[fmt.Sprint(int(id))] = id
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
	}
}

func startWiringNode(t *testing.T) (string, *wiringNode) {
	t.Helper()
	n := &wiringNode{next: 5,
		blocks: []obj{{"id": float64(0), "inputs": obj{"0": float64(0)}, "outputs": obj{}}, {"id": float64(1), "inputs": obj{"0": float64(1)}, "outputs": obj{"100001": float64(100001)}}},
		ios: []obj{
			{"id": float64(0), "io_class": "sacn", "io_type": "input", "universe": float64(1), "priority": float64(150)},
			{"id": float64(1), "io_class": "sacn", "io_type": "input", "universe": float64(2)},
			{"id": float64(100000), "io_class": "dmx", "io_type": "output", "port_number": float64(0)},
			{"id": float64(100001), "io_class": "dmx", "io_type": "output", "port_number": float64(1)},
		}}
	srv := httptest.NewServer(n)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), n
}

func u(n int) *int { return &n }

func TestSetBlockInputSlots(t *testing.T) {
	ip, n := startWiringNode(t)

	// Slot 1 on the universe it already has: nothing sent, so its priority is kept.
	if err := setBlockInput(ip, "0", blockInput{Slot: 0, Protocol: "sacn", Universe: u(1)}); err != nil {
		t.Fatal(err)
	}
	if len(n.calls) != 0 {
		t.Errorf("same universe sent %v", n.calls)
	}

	// A second slot: U2 is attached to the other block, so it gets its own IO.
	if err := setBlockInput(ip, "0", blockInput{Slot: 1, Protocol: "sacn", Universe: u(2)}); err != nil {
		t.Fatal(err)
	}
	if got := n.blocks[0]["inputs"].(obj)["1"]; got != float64(5) {
		t.Errorf("slot 2 got IO %v, want the new one", got)
	}

	// Moving slot 1 elsewhere removes the old input first, even though the node then
	// deletes the IO it was on.
	if err := setBlockInput(ip, "0", blockInput{Slot: 0, Protocol: "artnet", Universe: u(0)}); err != nil {
		t.Fatal(err)
	}
	for _, io := range n.ios {
		if io["id"] == float64(0) {
			t.Error("stand-in should have deleted the unused IO")
		}
	}
	if got := n.blocks[0]["inputs"].(obj)["0"]; got != float64(6) {
		t.Errorf("slot 1 got IO %v", got)
	}

	// And back to the universe whose IO has gone: it is created again.
	if err := setBlockInput(ip, "0", blockInput{Slot: 0, Protocol: "sacn", Universe: u(1)}); err != nil {
		t.Fatal(err)
	}
	if got := n.blocks[0]["inputs"].(obj)["0"]; got != float64(7) {
		t.Errorf("slot 1 got IO %v", got)
	}

	if err := setBlockInput(ip, "0", blockInput{Slot: 1, Clear: true}); err != nil {
		t.Fatal(err)
	}
	if _, still := n.blocks[0]["inputs"].(obj)["1"]; still {
		t.Error("slot 2 not cleared")
	}
	before := len(n.calls)
	if err := setBlockInput(ip, "0", blockInput{Slot: 3, Clear: true}); err != nil || len(n.calls) != before {
		t.Errorf("clearing an empty slot: err %v, calls %v", err, n.calls[before:])
	}
}

func TestAddBlockOutputs(t *testing.T) {
	ip, n := startWiringNode(t)

	if err := addBlockOutput(ip, "0", blockOutput{Protocol: "sacn", Universe: u(9)}); err != nil {
		t.Fatal(err)
	}
	var made obj
	for _, io := range n.ios {
		if io["universe"] == float64(9) {
			made = io
		}
	}
	if made == nil || made["io_type"] != "output" || made["io_class"] != "sacn" {
		t.Fatalf("sACN output IO = %v", made)
	}
	if err := addBlockOutput(ip, "0", blockOutput{Protocol: "artnet", Universe: u(0)}); err != nil {
		t.Fatal(err)
	}
	if err := addBlockOutput(ip, "0", blockOutput{Protocol: "dmx", Port: u(1)}); err != nil {
		t.Fatal(err)
	}
	if got := len(n.blocks[0]["outputs"].(obj)); got != 3 {
		t.Errorf("want 3 outputs on the block, got %v", n.blocks[0]["outputs"])
	}

	// Adding what it already has sends nothing.
	before := len(n.calls)
	if err := addBlockOutput(ip, "0", blockOutput{Protocol: "sacn", Universe: u(9)}); err != nil {
		t.Fatal(err)
	}
	if err := addBlockOutput(ip, "0", blockOutput{Protocol: "dmx", Port: u(1)}); err != nil {
		t.Fatal(err)
	}
	if len(n.calls) != before {
		t.Errorf("repeats sent %v", n.calls[before:])
	}

	// A DMX port another block drives is refused, by name.
	err := addBlockOutput(ip, "0", blockOutput{Protocol: "dmx", Port: u(2)})
	if err == nil || !strings.Contains(err.Error(), "already the output of PB 1") {
		t.Errorf("err = %v", err)
	}
}

func TestWiringChecksBeforeSending(t *testing.T) {
	for _, in := range []blockInput{{Slot: 0, Protocol: "nope", Universe: u(1)}, {Slot: 0, Protocol: "sacn"}, {Slot: 0, Protocol: "sacn", Universe: u(0)}, {Slot: 0, Protocol: "artnet", Universe: u(40000)}, {Slot: 999, Clear: true}} {
		if in.check() == nil {
			t.Errorf("%+v should be refused", in)
		}
	}
	for _, o := range []blockOutput{{Protocol: "nope"}, {Protocol: "dmx"}, {Protocol: "dmx", Port: u(0)}, {Protocol: "sacn", Universe: u(0)}} {
		if o.check() == nil {
			t.Errorf("%+v should be refused", o)
		}
	}
	if (blockInput{Slot: 1, Protocol: "artnet", Universe: u(0)}).check() != nil || (blockOutput{Protocol: "dmx", Port: u(1)}).check() != nil {
		t.Error("valid wiring refused")
	}
}

// The node answers OK to slots a mode does not take and then drops them, so the limit
// has to be checked before sending.
func TestSlotLimitIsCheckedBeforeSending(t *testing.T) {
	for mode, limit := range map[string]int{"REROUTE": 1, "XFADE": 2, "BACKUP": 2, "HTP": 4, "SWITCH": 4} {
		ip, n := startWiringNode(t)
		n.blocks[0]["mode"] = mode
		err := setBlockInput(ip, "0", blockInput{Slot: limit, Protocol: "sacn", Universe: u(50)})
		if err == nil || !strings.Contains(err.Error(), "takes") {
			t.Errorf("%s slot %d: err = %v", mode, limit+1, err)
		}
		if len(n.calls) != 0 {
			t.Errorf("%s sent %v", mode, n.calls)
		}
		if err := setBlockInput(ip, "0", blockInput{Slot: limit - 1, Protocol: "sacn", Universe: u(50)}); limit > 1 && err != nil {
			t.Errorf("%s slot %d refused: %v", mode, limit, err)
		}
	}
}

func TestSetBlockControl(t *testing.T) {
	ip, n := startWiringNode(t)
	n.blocks = append(n.blocks, obj{"id": float64(2), "inputs": obj{}, "outputs": obj{}})
	const other = float64(55)
	n.blocks[0]["mode"], n.blocks[0]["xfade_ctrl"] = "XFADE", float64(-1)
	n.blocks[1]["xfade_ctrl"] = float64(-1)
	n.blocks[2]["xfade_ctrl"] = other // already has its own control, which must be left alone
	// A plain input on the same universe exists; it must not be used for the control.
	n.ios = append(n.ios, obj{"id": float64(4), "io_class": "sacn", "io_type": "input", "universe": float64(70)})

	if err := setBlockControl(ip, "0", blockControl{Role: "xfade", Protocol: "sacn", Universe: u(70)}); err != nil {
		t.Fatal(err)
	}
	id := n.blocks[0]["xfade_ctrl"].(float64)
	if id == 4 || id < 0 {
		t.Fatalf("control on IO %v", id)
	}
	var made obj
	for _, io := range n.ios {
		if io["id"] == id {
			made = io
		}
	}
	if made["io_type"] != "x-fade-ctrl" {
		t.Errorf("control IO = %v", made)
	}
	// The node spread it to the other unset block; it must have been taken back off.
	if n.blocks[1]["xfade_ctrl"] != float64(-1) {
		t.Errorf("the new control stayed on another block: %v", n.blocks[1]["xfade_ctrl"])
	}
	if n.blocks[2]["xfade_ctrl"] != other {
		t.Errorf("a block's own control was changed: %v", n.blocks[2]["xfade_ctrl"])
	}

	// A second block on the same universe shares that IO, so nothing new is created.
	ios := len(n.ios)
	if err := setBlockControl(ip, "1", blockControl{Role: "xfade", Protocol: "sacn", Universe: u(70)}); err != nil {
		t.Fatal(err)
	}
	if len(n.ios) != ios || n.blocks[1]["xfade_ctrl"] != id {
		t.Errorf("sharing: %d IOs (was %d), block 1 on %v", len(n.ios), ios, n.blocks[1]["xfade_ctrl"])
	}

	before := len(n.calls)
	if err := setBlockControl(ip, "0", blockControl{Role: "xfade", Protocol: "sacn", Universe: u(70)}); err != nil || len(n.calls) != before {
		t.Errorf("same universe: err %v, sent %v", err, n.calls[before:])
	}

	if err := setBlockControl(ip, "0", blockControl{Role: "xfade", Clear: true}); err != nil {
		t.Fatal(err)
	}
	if n.blocks[0]["xfade_ctrl"] != float64(-1) {
		t.Errorf("not cleared: %v", n.blocks[0]["xfade_ctrl"])
	}
	before = len(n.calls)
	if err := setBlockControl(ip, "0", blockControl{Role: "xfade", Clear: true}); err != nil || len(n.calls) != before {
		t.Errorf("clearing nothing: err %v, sent %v", err, n.calls[before:])
	}

	for _, c := range []blockControl{{Role: "nope", Clear: true}, {Role: "xfade"}, {Role: "xfade", Protocol: "sacn"}, {Role: "switch", Protocol: "sacn", Universe: u(0)}} {
		if c.check() == nil {
			t.Errorf("%+v should be refused", c)
		}
	}
}

// The startup-buffer routes PUT a whole block back, so they must drop what the node
// only reports (id, inputs, outputs, status, sources, controls), as editBlock does.
func TestStartupBufferClearSendsOnlySettableFields(t *testing.T) {
	const current = `{"id":3,"name":"PB 4","mode":"HTP","timeout":5,"backup_auto_recover":false,"ratelimiting":true,
		"colors":[],"masterlimit":"NONE","startup_buffer":[7,7,7],"multi_channel_definitions":[],
		"inputs":{"0":11},"outputs":{"100011":100011},"status":{"x":1},"sources":{"inputs":[]},"x-fade-ctrl":null,"patches":[]}`
	target, seen := lumiServer(t, func(w http.ResponseWriter) { w.Write([]byte(current)) })
	// The route takes a bare address, so send every device connection to the stand-in.
	luminex.SetDeviceDialForTests(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, target)
	})
	t.Cleanup(func() { luminex.SetDeviceDialForTests((&net.Dialer{}).DialContext) })
	mux := http.NewServeMux()
	Mount(mux, Options{})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest("DELETE", srv.URL+"/api/node/192.168.200.3/startup/3", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	var put *lumiSeen
	for i := range *seen {
		if (*seen)[i].Method == "PUT" && (*seen)[i].Path == "/api/processblock/3" {
			put = &(*seen)[i]
		}
	}
	if put == nil {
		t.Fatalf("no PUT: %+v", *seen)
	}
	var sent obj
	if err := json.Unmarshal([]byte(put.Body), &sent); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "inputs", "outputs", "status", "sources", "x-fade-ctrl", "patches"} {
		if _, ok := sent[k]; ok {
			t.Errorf("PUT body carries %q, which the node does not take on a write", k)
		}
	}
	if buf, _ := sent["startup_buffer"].([]any); len(buf) != 0 || sent["name"] != "PB 4" || sent["mode"] != "HTP" {
		t.Errorf("sent %s", put.Body)
	}
}

// The node attaches a new control IO to every block with that control unset the moment it is created. If the
// assign that follows fails, it must still come back off the other blocks.
func TestFailedControlAssignStillCleansUp(t *testing.T) {
	ip, n := startWiringNode(t)
	n.blocks[0]["mode"], n.blocks[0]["xfade_ctrl"] = "XFADE", float64(-1)
	n.blocks[1]["xfade_ctrl"] = float64(-1)
	n.failCtrlPut = true
	if err := setBlockControl(ip, "0", blockControl{Role: "xfade", Protocol: "sacn", Universe: u(70)}); err == nil {
		t.Fatal("the refused assign was not reported")
	}
	if n.blocks[1]["xfade_ctrl"] != float64(-1) {
		t.Errorf("the new control stayed on another block: %v", n.blocks[1]["xfade_ctrl"])
	}
}

func TestOnlyIPv4AddressesAndNumbersReachADevice(t *testing.T) {
	for host, want := range map[string]bool{"192.168.1.8": true, "2.0.0.1": true, "attacker.example": false, "localhost": false,
		"192.168.1": false, "::1": false, "::ffff:192.168.1.8": false, "192.168.1.8:81": false, "": false} {
		if validHost(host) != want {
			t.Errorf("validHost(%q) = %v", host, !want)
		}
	}
	srv := mounted(t)
	for _, path := range []string{"/api/node/192.168.1.8/processblock/1%2F..%2F..%2Freset/raw", "/api/node/192.168.1.8/startup/x/snapshots"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Errorf("%s: %d, wanted 400", path, res.StatusCode)
		}
	}
}

// An empty long name is shown as "—". Saving a new short name must not write that dash to the node.
func TestDeviceNameKeepsAnEmptyLongName(t *testing.T) {
	var put string
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			b, _ := io.ReadAll(r.Body)
			put = string(b)
			return
		}
		w.Write([]byte(`{"ID":3,"short_name":"old","long_name":"","colors":[],"serial":"LUM111"}`))
	}))
	defer node.Close()
	luminex.SetDeviceDialForTests(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(node.URL, "http://"))
	})
	defer luminex.SetDeviceDialForTests((&net.Dialer{}).DialContext)
	srv := mounted(t)
	send := func(body string) int {
		req, _ := http.NewRequest("PUT", srv.URL+"/api/node/192.168.200.3/devicename", strings.NewReader(body))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := send(`{"short_name":"new","long_name":"—"}`); code != 200 || !strings.Contains(put, `"long_name":""`) || !strings.Contains(put, `"short_name":"new"`) {
		t.Errorf("%d, the node was sent %s", code, put)
	}

	// The tool lists another serial at this address: the write is refused and nothing is sent.
	put = ""
	options.Serial = func(string) string { return "LUM999" }
	defer func() { options.Serial = nil }()
	if code := send(`{"short_name":"x"}`); code != 409 || put != "" {
		t.Errorf("a write to another node's address: %d, sent %q", code, put)
	}
	options.Serial = func(string) string { return "lum111" }
	if code := send(`{"short_name":"x"}`); code != 200 || put == "" {
		t.Errorf("a write to the right node: %d, sent %q", code, put)
	}
}
