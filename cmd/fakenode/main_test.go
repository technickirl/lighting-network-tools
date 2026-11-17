package main

import (
	"fmt"
	"testing"
	"time"
)

// The seed must be self-consistent: every IO a block points at exists, DMX
// IOs sit at 100000+port, internal IOs at 101000+pb, every network IO has
// the v2.9.1 fields the page shows.
func TestSeedIsConsistent(t *testing.T) {
	nPorts = 12
	seed("LUM967718840", "Seed test")
	byID := map[int]obj{}
	for _, io := range ios {
		byID[int(toF(io["id"]))] = io
		switch io["io_class"] {
		case "dmx":
			p := int(toF(io["port_number"]))
			if int(toF(io["id"])) != dmxIOBase+p || io["name"] != fmt.Sprintf("Port %d", p+1) {
				t.Errorf("dmx IO %v: id/name off", io)
			}
		case "internal":
			if int(toF(io["id"])) != internalIOBase+int(toF(io["pb_number"])) {
				t.Errorf("internal IO %v: id off", io)
			}
		case "sacn", "artnet":
			for _, k := range []string{"source_ip", "group", "take_own_ip", "duplicate_ios"} {
				if _, ok := io[k]; !ok {
					t.Errorf("%s IO %v lacks %s", io["io_class"], io["id"], k)
				}
			}
		}
	}
	if len(pbs) != 16 {
		t.Fatalf("want 16 blocks, got %d", len(pbs))
	}
	modes := map[string]int{}
	for _, pb := range pbs {
		modes[pb["mode"].(string)]++
		for _, v := range pb["inputs"].(obj) {
			if byID[int(toF(v))] == nil {
				t.Errorf("block %v input points at missing IO %v", pb["id"], v)
			}
		}
		for k, v := range pb["outputs"].(obj) {
			if byID[int(toF(v))] == nil || k != fmt.Sprint(int(toF(v))) {
				t.Errorf("block %v output %s=%v: missing or key != id", pb["id"], k, v)
			}
		}
		for _, k := range []string{"summarized_active_input", "sources", "status", "takeover_active", "colors", "startup_buffer", "backup_ctrl", "masterlimit"} {
			if _, ok := pb[k]; !ok {
				t.Errorf("block %v lacks %s", pb["id"], k)
			}
		}
		if c := pb["backup_ctrl"]; toF(c) >= 0 && byID[int(toF(c))] == nil {
			t.Errorf("block %v backup_ctrl points at missing IO %v", pb["id"], c)
		}
	}
	if modes["REROUTE"] != 9 || modes["BACKUP"] != 1 || modes["HTP"] != 1 || modes["NONE"] != 5 {
		t.Errorf("modes: %v", modes)
	}
}

func TestLoadSamplesServesTheCaptures(t *testing.T) {
	if err := loadSamples("../../docs/samples"); err != nil {
		t.Fatal(err)
	}
	if len(pbs) != 16 || len(ios) != 40 || nPorts != 12 || nextIO != 12 {
		t.Errorf("blocks %d ios %d ports %d nextIO %d", len(pbs), len(ios), nPorts, nextIO)
	}
	if info["serial"] != "LUM553897152" {
		t.Errorf("deviceinfo not loaded: %v", info["serial"])
	}
}

func TestSeedWithFewPorts(t *testing.T) {
	for _, n := range []int{1, 2, 3} {
		nPorts = n
		seed("LUM653738368", "Small")
		if len(pbs) != n+4 {
			t.Errorf("%d ports: %d blocks", n, len(pbs))
		}
		for p := 0; p < n; p++ {
			if pbs[p]["mode"] != "REROUTE" {
				t.Errorf("%d ports: block %d is %v, want REROUTE", n, p, pbs[p]["mode"])
			}
		}
	}
}

func hasIO(id int) bool {
	for _, io := range ios {
		if int(toF(io["id"])) == id {
			return true
		}
	}
	return false
}

// The block write behaviour of a real LumiNode 12 (v2.9.1), so the fake refuses
// what the real node refuses.
func TestBlockWritesBehaveLikeARealNode(t *testing.T) {
	nPorts = 12
	seed("LUM967718840", "Block test")

	if msg := patchRefusal(obj{"name": "x"}); msg == "" {
		t.Error("PATCH without inputs and outputs should be refused")
	}
	if msg := patchRefusal(obj{"inputs": obj{}, "outputs": obj{}}); msg != "" {
		t.Errorf("PATCH with both refused: %s", msg)
	}

	before := fmt.Sprint(pbs[0])
	updateBlock(0, obj{"name": "Ignored"})
	if fmt.Sprint(pbs[0]) != before {
		t.Error("a PUT without a mode changed the block")
	}

	in, out := fmt.Sprint(pbs[0]["inputs"]), fmt.Sprint(pbs[0]["outputs"])
	updateBlock(0, obj{"mode": "HTP"})
	if pbs[0]["name"] != "" || toF(pbs[0]["timeout"]) != 1000 {
		t.Errorf("fields left out should go to their defaults: %v", pbs[0])
	}
	if fmt.Sprint(pbs[0]["inputs"]) != in || fmt.Sprint(pbs[0]["outputs"]) != out {
		t.Errorf("a mode change lost the block's IOs: %v", pbs[0])
	}

	if !hasIO(0) {
		t.Fatal("seed should have sACN IO 0")
	}
	updateBlock(0, obj{"mode": "NONE", "name": "Kept?"})
	if pbs[0]["mode"] != "NONE" || pbs[0]["name"] == "Kept?" || len(pbs[0]["inputs"].(obj)) != 0 || len(pbs[0]["outputs"].(obj)) != 0 {
		t.Errorf("mode NONE should clear the block: %v", pbs[0])
	}
	if hasIO(0) {
		t.Error("the IO mode NONE left unused should be deleted")
	}
	if !hasIO(dmxIOBase) {
		t.Error("a DMX IO must never be deleted")
	}
}

// A mode with fewer slots drops the inputs beyond them, as on a real node.
func TestModeWithFewerSlotsDropsInputs(t *testing.T) {
	nPorts = 12
	seed("LUM967718840", "Slot test")
	pbs[0]["inputs"] = obj{"0": 1, "1": 2, "2": 3, "3": 4}
	updateBlock(0, obj{"mode": "HTP"})
	if n := len(pbs[0]["inputs"].(obj)); n != 4 {
		t.Errorf("HTP kept %d", n)
	}
	updateBlock(0, obj{"mode": "XFADE"})
	if n := len(pbs[0]["inputs"].(obj)); n != 2 {
		t.Errorf("XFADE kept %d", n)
	}
	updateBlock(0, obj{"mode": "REROUTE"})
	if n := len(pbs[0]["inputs"].(obj)); n != 1 {
		t.Errorf("REROUTE kept %d", n)
	}
}

// A new control IO lands on every block with that control unset, as on a real node.
func TestNewControlIOSpreadsToUnsetBlocks(t *testing.T) {
	nPorts = 12
	seed("LUM967718840", "Control test")
	pbs[0]["xfade_ctrl"] = 77
	spreadNewControl(obj{"id": 40, "io_type": "x-fade-ctrl"})
	if toF(pbs[0]["xfade_ctrl"]) != 77 {
		t.Errorf("a set control was changed: %v", pbs[0]["xfade_ctrl"])
	}
	if toF(pbs[1]["xfade_ctrl"]) != 40 || toF(pbs[15]["xfade_ctrl"]) != 40 {
		t.Errorf("not spread: %v %v", pbs[1]["xfade_ctrl"], pbs[15]["xfade_ctrl"])
	}
	spreadNewControl(obj{"id": 41, "io_type": "input"})
	if toF(pbs[1]["backup_ctrl"]) >= 0 && pbs[1]["backup_ctrl"] == 41 {
		t.Error("a plain input spread")
	}
}

// With --rdm-search the fake searches like a real node: nothing listed until the
// first search is done, which misses the fixture still powering up; a forced
// discovery keeps the old list until it is done, then lists every fixture.
func TestRDMSearchLikeARealNode(t *testing.T) {
	nPorts = 4
	seed("LUM967718840", "Search test")
	defer func(d time.Duration) { rdmSearch = d; searches = map[int]*portSearch{} }(rdmSearch)
	rdmSearch = 60 * time.Millisecond
	io := findIO(dmxIOBase)
	io["rdm"] = false
	afterIOPut(io, true)
	io["rdm"] = true
	afterIOPut(io, false)
	_, all := rdmAt(0)
	if len(all) < 2 {
		t.Fatalf("port 1 needs two fixtures or more, has %d", len(all))
	}
	if got := found(0); len(got) != 0 {
		t.Errorf("listed %d while the first search runs, want 0", len(got))
	}
	time.Sleep(80 * time.Millisecond)
	if got := found(0); len(got) != len(all)-1 || got[all[len(all)-1].UID] {
		t.Errorf("first search listed %d (want %d, without the last fixture)", len(got), len(all)-1)
	}
	startSearch(0, false)
	if got := found(0); len(got) != len(all)-1 {
		t.Errorf("listed %d during a forced search, want the old %d", len(got), len(all)-1)
	}
	time.Sleep(80 * time.Millisecond)
	if got := found(0); len(got) != len(all) {
		t.Errorf("forced search listed %d, want all %d", len(got), len(all))
	}
	io["rdm"] = false
	afterIOPut(io, true)
	if searches[0] != nil {
		t.Error("RDM off left the search state")
	}
}
