package artnet

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

// Packets captured from LumiNode 12 - 8 (v2.9.1, 192.168.12.8) on 12 Nov 2026,
// DMX port 1 fed by sACN universe 1, with a Creamsource Vortex 8S on the line.
// The controller UID used then was 7FF0:000000C1.
const (
	realPollReply   = "4172742d4e6574000021c0a80c0836190209000029db00e24c4c4c756d694e6f6465203132202d20380000004c756d694e6f6465203132202d2038000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000002330303031205b305d2044657669636520697320756e6c6f636b65640000000000000000000000000000000000000000000000000000000000000000000000000001800000000800000081000000000000000100000000000000000000d0699ea40319c0a80c0801190000000000000000000000000000000000000000000000000000"
	realTodRequest  = "4172742d4e6574000080000e0000000000000000000000010100000000000000000000000000000000000000000000000000000000000000"
	realTodData     = "4172742d4e6574000081000e010100000000000001000001000100014f75000dc113"
	realInfoRequest = "4172742d4e6574000083000e01000000000000000000000101184f75000dc1137ff0000000c1010100000020006000053c"
	realInfoReply   = "4172742d4e6574000083000e010000000000000000000001012b7ff0000000c14f75000dc11301000000002100601301000301010100030100000a014b000100000505c9"
	realLabelReply  = "4172742d4e6574000083000e01000000000000000000000101257ff0000000c14f75000dc11304000000002100820d4353562d385320393031333935087a"
	realPersReply   = "4172742d4e6574000083000e010000000000000000000001011a7ff0000000c14f75000dc11307000000002100e002014b0612"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const vortex = UID(0x4F75000DC113)

func TestRealPollReply(t *testing.T) {
	r, err := ParsePollReply(unhex(t, realPollReply))
	if err != nil {
		t.Fatal(err)
	}
	if r.ShortName != "LumiNode 12 - 8" || r.BindIndex != 1 || !r.RDM || r.IP.String() != "192.168.12.8" || len(r.Ports) != 1 {
		t.Fatalf("%+v", r)
	}
	p := r.Ports[0]
	if !p.Output || p.Input || p.Address != 1 || !p.Sending || !p.SACN {
		t.Errorf("port %+v", p)
	}
}

func TestTodRequestMatchesWhatTheNodeAnswered(t *testing.T) {
	if got := hex.EncodeToString(TodRequestPacket(1)); got != realTodRequest {
		t.Errorf("got  %s\nwant %s", got, realTodRequest)
	}
}

func TestRealTodData(t *testing.T) {
	d, err := ParseTodData(unhex(t, realTodData))
	if err != nil {
		t.Fatal(err)
	}
	if d.Address != 1 || d.UIDTotal != 1 || d.Nak || len(d.UIDs) != 1 || d.UIDs[0] != vortex {
		t.Fatalf("%+v", d)
	}
	// And the fake builds the same packet.
	if got := hex.EncodeToString(TodDataPacket(1, 1, 1, 0, []UID{vortex})); got != realTodData {
		t.Errorf("fake ArtTodData\n got %s\nwant %s", got, realTodData)
	}
}

func TestRdmRequestMatchesWhatTheFixtureAnswered(t *testing.T) {
	req := Request{Dest: vortex, Src: 0x7FF0000000C1, TN: 1, CC: CCGet, PID: PIDDeviceInfo}
	if got := hex.EncodeToString(RdmPacket(1, req.Encode())); got != realInfoRequest {
		t.Errorf("got  %s\nwant %s", got, realInfoRequest)
	}
}

func TestRealRdmReplies(t *testing.T) {
	parse := func(s string) Response {
		pa, m, err := ParseRdmPacket(unhex(t, s))
		if err != nil || pa != 1 {
			t.Fatalf("pa=%v err=%v", pa, err)
		}
		r, err := ParseResponse(m)
		if err != nil {
			t.Fatal(err)
		}
		if r.Src != vortex || r.Dest != 0x7FF0000000C1 || r.Type != RespAck || r.CC != CCGetResponse {
			t.Fatalf("%+v", r)
		}
		return r
	}
	info, err := ParseDeviceInfo(parse(realInfoReply).Data)
	if err != nil {
		t.Fatal(err)
	}
	want := DeviceInfo{ProtocolVersion: 0x0100, Model: 0x0301, Category: 0x0101, SoftwareVersion: 0x00030100, Footprint: 10,
		Personality: 1, PersonalityCount: 75, StartAddress: 1, Sensors: 5}
	if info != want {
		t.Errorf("DEVICE_INFO %+v\nwant %+v", info, want)
	}
	if CategoryName(info.Category) != "Fixture" {
		t.Errorf("category %q", CategoryName(info.Category))
	}
	if l := Text(parse(realLabelReply).Data); l != "CSV-8S 901395" {
		t.Errorf("label %q", l)
	}
	if p := parse(realPersReply).Data; len(p) != 2 || p[0] != 1 || p[1] != 75 {
		t.Errorf("personality %x", p)
	}
}

func TestBadChecksumIsRefused(t *testing.T) {
	b := unhex(t, realLabelReply)
	b[len(b)-1] ^= 1
	_, m, _ := ParseRdmPacket(b)
	if _, err := ParseResponse(m); err == nil {
		t.Error("a bad checksum was accepted")
	}
}

func TestUIDForms(t *testing.T) {
	for _, s := range []string{"4F:75:00:0D:C1:13", "4F75:000DC113", "4f75000dc113"} {
		if u, err := ParseUID(s); err != nil || u != vortex {
			t.Errorf("%s -> %v %v", s, u, err)
		}
	}
	if vortex.String() != "4F75:000DC113" {
		t.Errorf("%s", vortex)
	}
	if _, err := ParseUID("4F:75"); err == nil {
		t.Error("short UID accepted")
	}
}

// A controller against the fake node, both on localhost.
func fakePair(t *testing.T, ports map[PortAddress][]*FakeFixture) (*Controller, *FakeNode) {
	t.Helper()
	f, err := ServeFake("127.0.0.1:0", ports, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	c, err := Open("127.0.0.1:0", f.Addr().Port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.Timeout = 300 * time.Millisecond
	return c, f
}

func testFixture(uid UID, start uint16) *FakeFixture {
	return &FakeFixture{UID: uid, Manufacturer: "Creamsource", Model: "Vortex 8S", ModelID: 0x0301, Category: 0x0101,
		Label: "CSV " + uid.String(), Software: "3.1.0", Start: start, Personality: 1,
		Personalities: []FakePersonality{{10, "Mode 1"}, {16, "Mode 2"}}, Sensors: 5}
}

func TestControllerAgainstFake(t *testing.T) {
	a, b := testFixture(vortex, 1), testFixture(vortex+1, 11)
	b.AckTimerOnce = true
	c, f := fakePair(t, map[PortAddress][]*FakeFixture{21: {a, b}})

	uids, err := c.Tod("127.0.0.1", 21, time.Second)
	if err != nil || len(uids) != 2 || uids[0] != vortex || uids[1] != vortex+1 {
		t.Fatalf("Tod = %v, %v", uids, err)
	}
	if _, err := c.Tod("127.0.0.1", 22, 300*time.Millisecond); !errors.Is(err, ErrNoTod) {
		t.Errorf("Tod on an address with no RDM: %v", err)
	}

	d, err := c.Get("127.0.0.1", 21, vortex, PIDDeviceInfo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := ParseDeviceInfo(d); info.StartAddress != 1 || info.Footprint != 10 || info.PersonalityCount != 2 {
		t.Errorf("%+v", info)
	}
	// ACK_TIMER: the label comes back through QUEUED_MESSAGE.
	if d, err := c.Get("127.0.0.1", 21, vortex+1, PIDDeviceLabel, nil); err != nil || Text(d) != b.Label {
		t.Errorf("label after ACK_TIMER = %q, %v", d, err)
	}
	// NACK comes back as a NackError.
	var nack *NackError
	if _, err := c.Get("127.0.0.1", 21, vortex, 0x8123, nil); !errors.As(err, &nack) || nack.Reason != 0 {
		t.Errorf("unknown PID: %v", err)
	}
	if d, err := c.Get("127.0.0.1", 21, vortex, PIDDMXPersonalityDescription, []byte{2}); err != nil || Text(d[3:]) != "Mode 2" {
		t.Errorf("personality 2 = %q, %v", d, err)
	}
	// A device that does not answer: one retry, then ErrNoReply.
	f.Lock()
	a.Silent = true
	before := f.Requests
	f.Unlock()
	if _, err := c.Get("127.0.0.1", 21, vortex, PIDDeviceInfo, nil); !errors.Is(err, ErrNoReply) {
		t.Errorf("silent device: %v", err)
	}
	f.Lock()
	if n := f.Requests - before; n != 2 {
		t.Errorf("silent device got %d requests, want 2 (one retry)", n)
	}
	f.Unlock()
}

func TestTodWithManyBlocks(t *testing.T) {
	var fx []*FakeFixture
	for i := 0; i < 450; i++ {
		fx = append(fx, testFixture(vortex+UID(i), 1))
	}
	c, _ := fakePair(t, map[PortAddress][]*FakeFixture{5: fx})
	uids, err := c.Tod("127.0.0.1", 5, 2*time.Second)
	if err != nil || len(uids) != 450 {
		t.Fatalf("got %d UIDs, %v", len(uids), err)
	}
}

func TestDryRunDoesNotSendSet(t *testing.T) {
	old := luminex.DryRun
	luminex.DryRun = true
	t.Cleanup(func() { luminex.DryRun = old })
	c, f := fakePair(t, map[PortAddress][]*FakeFixture{1: {testFixture(vortex, 1)}})
	if _, err := c.Do("127.0.0.1", 1, Request{Dest: vortex, CC: CCSet, PID: PIDIdentifyDevice, Data: []byte{1}}); !errors.Is(err, luminex.ErrDryRun) {
		t.Errorf("SET under DRY_RUN: %v", err)
	}
	if _, err := c.Get("127.0.0.1", 1, vortex, PIDDeviceInfo, nil); err != nil {
		t.Errorf("GET under DRY_RUN should still go out: %v", err)
	}
	f.Lock()
	defer f.Unlock()
	if f.Requests != 1 {
		t.Errorf("fake saw %d requests, want 1 (the GET)", f.Requests)
	}
}

// A broadcast UID would reach every fixture on the port and none would reply,
// so the controller never sends one, GET or SET.
func TestBroadcastUIDIsNotSent(t *testing.T) {
	c, f := fakePair(t, map[PortAddress][]*FakeFixture{1: {testFixture(vortex, 1)}})
	for _, s := range []string{"FFFF:FFFFFFFF", "4F75:FFFFFFFF"} {
		u, err := ParseUID(s)
		if err != nil {
			t.Fatal(err)
		}
		if !u.Broadcast() {
			t.Errorf("%s: Broadcast() false", s)
		}
		if err := c.Set("127.0.0.1", 1, u, PIDFactoryDefaults, nil); err == nil || !strings.Contains(err.Error(), "broadcast") {
			t.Errorf("SET to %s: %v", s, err)
		}
		if _, err := c.Get("127.0.0.1", 1, u, PIDDeviceInfo, nil); err == nil {
			t.Errorf("GET to %s went out", s)
		}
	}
	if vortex.Broadcast() {
		t.Error("a fixture's own UID reads as broadcast")
	}
	f.Lock()
	defer f.Unlock()
	if f.Requests != 0 {
		t.Errorf("fake saw %d requests, want 0", f.Requests)
	}
}

func TestPortInUseIsSaidPlainly(t *testing.T) {
	c, err := Open("127.0.0.1:0", Port)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = Open(c.LocalAddr().String(), Port)
	if err == nil {
		t.Fatal("second bind succeeded")
	}
	if want := "in use by another program"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not say %q", err, want)
	}
}

func TestSetsAgainstFake(t *testing.T) {
	fx := testFixture(vortex, 1)
	c, f := fakePair(t, map[PortAddress][]*FakeFixture{1: {fx}})
	if err := c.Set("127.0.0.1", 1, vortex, PIDDMXStartAddress, []byte{0x01, 0x2C}); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("127.0.0.1", 1, vortex, PIDIdentifyDevice, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if d, err := c.Get("127.0.0.1", 1, vortex, PIDIdentifyDevice, nil); err != nil || len(d) != 1 || d[0] != 1 {
		t.Errorf("identify reads %x, %v", d, err)
	}
	var nack *NackError
	if err := c.Set("127.0.0.1", 1, vortex, PIDDMXPersonality, []byte{9}); !errors.As(err, &nack) || nack.Reason != 6 {
		t.Errorf("personality 9 of 2: %v", err)
	}
	f.Lock()
	defer f.Unlock()
	if fx.Start != 300 || !fx.Identify || fx.Personality != 1 {
		t.Errorf("fixture now %+v", fx)
	}
}

// Two DMX ports on one universe each answer with their own table; TodTables
// keeps them apart and returns as soon as it has as many as asked for.
func TestTodTablesKeepsEachPortApart(t *testing.T) {
	a, b, d := testFixture(vortex, 1), testFixture(vortex+1, 11), testFixture(vortex+2, 21)
	c, f := fakePair(t, map[PortAddress][]*FakeFixture{33: {d}})
	f.Lock()
	f.SharedPorts = map[PortAddress][][]*FakeFixture{33: {{a, b}}}
	f.TodBinds = map[PortAddress][]int{33: {2, 255}} // as LumiNode 12 - 4 sent them
	f.Unlock()
	start := time.Now()
	tables, err := c.TodTables("127.0.0.1", 33, 2, time.Second)
	if err != nil || len(tables) != 2 || tables[0].Bind != 2 || len(tables[0].UIDs) != 2 || tables[1].Bind != 255 || len(tables[1].UIDs) != 1 {
		t.Errorf("tables = %+v, %v", tables, err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("waited for the timeout: %v", time.Since(start))
	}
	// Asked for three, two come: what came, after the wait.
	if tables, err := c.TodTables("127.0.0.1", 33, 3, 200*time.Millisecond); err != nil || len(tables) != 2 {
		t.Errorf("short = %+v, %v", tables, err)
	}
}

// Both ports' tables with the same BindIndex, as LumiNode 12 - 5 sent them for
// ports 9 and 10 on U19 (19 Nov 2026): still two tables, not one merged.
func TestTodTablesWithTheSameBindIndex(t *testing.T) {
	a, b, d := testFixture(vortex, 1), testFixture(vortex+1, 11), testFixture(vortex+2, 21)
	c, f := fakePair(t, map[PortAddress][]*FakeFixture{19: {d}})
	f.Lock()
	f.SharedPorts = map[PortAddress][][]*FakeFixture{19: {{a, b}}}
	f.TodBinds = map[PortAddress][]int{19: {255, 255}}
	f.Unlock()
	start := time.Now()
	tables, err := c.TodTables("127.0.0.1", 19, 2, time.Second)
	if err != nil || len(tables) != 2 || len(tables[0].UIDs) != 2 || len(tables[1].UIDs) != 1 || tables[1].UIDs[0] != d.UID {
		t.Errorf("tables = %+v, %v", tables, err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("waited for the timeout: %v", time.Since(start))
	}
}
