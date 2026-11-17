package nodeconfig

// Driven by the raw captures from a real LumiNode 12 (firmware v2.9.1) in
// docs/samples: the ground truth for how block IO ids resolve into words.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadSample(t *testing.T, suffix string) []obj {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "samples", "luminode12-v2.9.1-"+suffix+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var list []obj
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return list
}

func TestRealNodeBlocksResolveToWords(t *testing.T) {
	pbs := normaliseProcessBlocks(loadSample(t, "processblock"), loadSample(t, "IO"))
	if len(pbs) != 16 {
		t.Fatalf("want 16 blocks, got %d", len(pbs))
	}
	b := pbs[0]
	if b.Mode != "REROUTE" || b.Name != "ProcessEngine 1" {
		t.Errorf("block 0: mode %q name %q", b.Mode, b.Name)
	}
	if len(b.Inputs) != 1 || len(b.Outputs) != 1 {
		t.Fatalf("block 0: %d inputs, %d outputs", len(b.Inputs), len(b.Outputs))
	}
	in := b.Inputs[0]
	if in.Slot != 0 || in.IO.ID != 0 || in.IO.Label != "sACN U1" || in.IO.Detail != "prio 100" || in.IO.Name != "" {
		t.Errorf("block 0 input: %+v", in)
	}
	out := b.Outputs[0]
	if out.ID != 100000 || out.Label != "DMX Port 1" || out.Name != "Port 1" || out.IoClass != "dmx" {
		t.Errorf("block 0 output: %+v", out)
	}
	if b.Flow != "sACN U1 (prio 100) → DMX Port 1" {
		t.Errorf("block 0 flow: %q", b.Flow)
	}
	if b.Color != "#00ffa1" || b.StartupBuffer != 0 || b.ActiveInput != nil || len(b.Controls) != 0 || !b.Configured {
		t.Errorf("block 0 extras: color %q startup %d active %v controls %v configured %v", b.Color, b.StartupBuffer, b.ActiveInput, b.Controls, b.Configured)
	}
	// Block 11 is the last REROUTE one: sACN U12 → DMX Port 12.
	if pbs[11].Flow != "sACN U12 (prio 100) → DMX Port 12" {
		t.Errorf("block 11 flow: %q", pbs[11].Flow)
	}
	// Blocks 12-15 are NONE with nothing attached: they collapse on the page.
	configured := 0
	for _, pb := range pbs {
		if pb.Configured {
			configured++
		}
	}
	if configured != 12 {
		t.Errorf("want 12 configured blocks, got %d", configured)
	}
	if pbs[12].Mode != "NONE" || pbs[12].Flow != "" || pbs[12].Configured {
		t.Errorf("block 12: %+v", pbs[12])
	}
	// The v28 fields the edit flows read are unchanged.
	if len(b.InputIDs) != 1 || b.InputIDs[0] != 0 || len(b.OutputIDs) != 1 || b.OutputIDs[0] != 100000 {
		t.Errorf("block 0 raw ids: inputs %v outputs %v", b.InputIDs, b.OutputIDs)
	}
}

func TestRealNodeIOsKnowTheirBlock(t *testing.T) {
	ios := normaliseIOs(loadSample(t, "IO"), loadSample(t, "processblock"))
	if len(ios) != 40 {
		t.Fatalf("want 40 IOs, got %d", len(ios))
	}
	byID := map[int]IO{}
	for _, io := range ios {
		byID[io.ID] = io
	}
	u1 := byID[0]
	if u1.Label != "sACN U1" || u1.Name != "sACN U1" || u1.Universe != 1 || u1.Priority != 100 || u1.Group != 0 || u1.TakeOwnIP != true || u1.IP != "0.0.0.0" {
		t.Errorf("sACN U1: %+v", u1)
	}
	if !u1.InUse || len(u1.UsedBy) != 1 || u1.UsedBy[0] != (IOUse{PB: 0, PBName: "ProcessEngine 1", Role: "input", Slot: 0}) {
		t.Errorf("sACN U1 usedBy: %+v", u1.UsedBy)
	}
	p1 := byID[100000]
	if p1.Label != "DMX Port 1" || p1.Name != "Port 1" || p1.PortNumber != 0 || p1.RDM != "OFF" || !p1.Interweaving || p1.StreamActivityTime != 6000 {
		t.Errorf("DMX Port 1: %+v", p1)
	}
	if !p1.InUse || len(p1.UsedBy) != 1 || p1.UsedBy[0] != (IOUse{PB: 0, PBName: "ProcessEngine 1", Role: "output"}) {
		t.Errorf("DMX Port 1 usedBy: %+v", p1.UsedBy)
	}
	internal := byID[101003]
	if internal.IoClass != "internal" || internal.Label != "Internal PB 4" || internal.PBNumber != 3 || internal.InUse {
		t.Errorf("internal IO: %+v", internal)
	}
	if p1.Group != nil || p1.TakeOwnIP != nil || internal.Universe != nil {
		t.Errorf("fields that do not apply must be null: dmx group %v takeOwnIp %v, internal universe %v", p1.Group, p1.TakeOwnIP, internal.Universe)
	}
	inUse := 0
	for _, io := range ios {
		if io.InUse {
			inUse++
		}
	}
	if inUse != 24 { // 12 sACN inputs + 12 DMX outputs
		t.Errorf("want 24 IOs in use, got %d", inUse)
	}
}

// A merge block the way the fake node seeds one: BACKUP with two inputs, the
// active one flagged, a control input, a sender seen on slot 0.
func mergeFixture() ([]obj, []obj) {
	ios := []obj{
		{"id": 5.0, "name": "Desk A main", "io_class": "sacn", "io_type": "input", "universe": 101.0, "priority": 150.0, "group": 0.0, "take_own_ip": true, "source_ip": "0.0.0.0", "duplicate_ios": []any{6.0}},
		{"id": 6.0, "name": "Desk B backup", "io_class": "sacn", "io_type": "input", "universe": 101.0, "priority": 100.0, "group": 0.0, "take_own_ip": true, "source_ip": "0.0.0.0", "duplicate_ios": []any{5.0}},
		{"id": 7.0, "name": "Media server", "io_class": "artnet", "io_type": "input", "universe": 0.0, "group": 2.0, "take_own_ip": false, "source_ip": "10.0.0.5", "duplicate_ios": []any{}},
		{"id": 100003.0, "name": "Port 4", "io_class": "dmx", "io_type": "output", "port_number": 3.0, "rdm": true, "rdm_universe": 1.0, "interweaving": true, "stream_activity_time": 6000.0},
	}
	pbs := []obj{{
		"id": 3.0, "name": "FOH backup", "mode": "BACKUP", "colors": []any{"#8050d8"},
		"inputs": map[string]any{"0": 5.0, "1": 6.0}, "outputs": map[string]any{"100003": 100003.0},
		"backup_ctrl": 7.0, "switch_ctrl": -1.0, "xfade_ctrl": -1.0, "masterlimit": "NONE", "masterlimit_ctrl": -1.0,
		"timeout": 2500.0, "backup_auto_recover": false, "ratelimiting": true,
		"summarized_active_input": 1.0, "takeover_active": false,
		"sources": map[string]any{"inputs": []any{
			[]any{map[string]any{"ip": "192.168.1.10", "name": "Desk A", "priority": 150.0}},
			map[string]any{"ip": "192.168.1.20", "name": "Desk B", "priority": 100.0}, nil, nil}},
		"startup_buffer": make([]any, 512),
	}}
	return pbs, ios
}

func TestMergeBlockSlotsControlsAndSources(t *testing.T) {
	pbList, ioList := mergeFixture()
	b := normaliseProcessBlocks(pbList, ioList)[0]
	if b.Mode != "BACKUP" || b.Timeout != 2500 || b.Backup {
		t.Errorf("mode/timeout/auto-recover: %q %d %v", b.Mode, b.Timeout, b.Backup)
	}
	if len(b.Inputs) != 2 {
		t.Fatalf("inputs: %+v", b.Inputs)
	}
	if b.Inputs[0].Active || !b.Inputs[1].Active || b.ActiveInput != 1 {
		t.Errorf("active slot: %+v %+v (activeInput %v)", b.Inputs[0], b.Inputs[1], b.ActiveInput)
	}
	if got := b.Inputs[0].IO.Text(); got != "Desk A main · sACN U101 (prio 150)" {
		t.Errorf("slot 0 text: %q", got)
	}
	if got := b.Inputs[0].Sources; len(got) != 1 || got[0] != "192.168.1.10 (Desk A, prio 150)" {
		t.Errorf("slot 0 sources: %v", got)
	}
	if got := b.Inputs[1].Sources; len(got) != 1 || got[0] != "192.168.1.20 (Desk B, prio 100)" {
		t.Errorf("slot 1 sources: %v", got)
	}
	if len(b.Controls) != 1 || b.Controls[0].Role != "backup" || b.Controls[0].IO.Text() != "Media server · Art-Net U0 (group 2)" {
		t.Errorf("controls: %+v", b.Controls)
	}
	if b.Outputs[0].Text() != "DMX Port 4 (RDM)" {
		t.Errorf("output text: %q", b.Outputs[0].Text())
	}
	if b.Flow != "Desk A main · sACN U101 (prio 150) + ● Desk B backup · sACN U101 (prio 100) → DMX Port 4 (RDM)" {
		t.Errorf("flow: %q", b.Flow)
	}
	if b.StartupBuffer != 512 || b.Color != "#8050d8" {
		t.Errorf("startup %d color %q", b.StartupBuffer, b.Color)
	}

	ios := normaliseIOs(ioList, pbList)
	if ios[0].UsedBy[0] != (IOUse{PB: 3, PBName: "FOH backup", Role: "input", Slot: 0}) || ios[1].UsedBy[0].Slot != 1 {
		t.Errorf("input usedBy: %+v / %+v", ios[0].UsedBy, ios[1].UsedBy)
	}
	if len(ios[0].DuplicateIOs) != 1 || ios[0].DuplicateIOs[0] != 6 {
		t.Errorf("duplicate_ios: %v", ios[0].DuplicateIOs)
	}
	if !ios[2].InUse || ios[2].UsedBy[0].Role != "backup" || ios[2].TakeOwnIP != false || ios[2].Group != 2 || ios[2].IP != "10.0.0.5" {
		t.Errorf("art-net control IO: %+v", ios[2])
	}
	if ios[3].UsedBy[0].Role != "output" || ios[3].RDM != "ON" || ios[3].RDMUniverse != 1 {
		t.Errorf("dmx output IO: %+v", ios[3])
	}
}

func TestMissingIOAndNoIOList(t *testing.T) {
	pb := []obj{{"id": 2.0, "mode": "REROUTE", "inputs": map[string]any{"0": 41.0}, "outputs": map[string]any{"9": 9.0}}}
	b := normaliseProcessBlocks(pb, nil)[0]
	if !b.Inputs[0].IO.Missing || b.Inputs[0].IO.Label != "IO 41" || b.Flow != "IO 41 → IO 9" {
		t.Errorf("unresolved block: %+v flow %q", b.Inputs[0].IO, b.Flow)
	}
	if b.Name != "Process Block 2" {
		t.Errorf("default name: %q", b.Name)
	}
}

func TestDescribeIOOtherClasses(t *testing.T) {
	cases := map[string]obj{
		"Art-Net U3":    {"io_class": "artnet", "universe": 3.0},
		"Internal PB 1": {"io_class": "internal", "pb_number": 0.0},
		"player U2":     {"io_class": "player", "universe": 2.0, "id": 50.0},
		"kinet IO 51":   {"io_class": "kinet", "id": 51.0},
		"IO 52":         {"id": 52.0},
		"DMX IO 53":     {"io_class": "dmx", "id": 53.0},
	}
	for want, io := range cases {
		if got := describeIO(io).Label; got != want {
			t.Errorf("describeIO(%v) = %q, want %q", io, got, want)
		}
	}
	if got := (IORef{Label: "DMX Port 1", Name: "Port 1"}).Text(); got != "DMX Port 1" {
		t.Errorf("a name the label already contains is not repeated: %q", got)
	}
}
