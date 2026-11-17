package commission

import (
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"testing"
)

// Captured from a real GigaCore 12 (v3.1.5), 4 Nov 2026.
const gen1Sample = "Port 1?D0-69-9E-4B-73-CD?eth1?LUM491425257?eth1?Other(-), Bridge(+)?192.168.2.4 (IPv4)\xa4 FFFF000000000000FF69FFFFFFFFFF1E (IPv6)|" +
	"Port 2?D0-69-9E-AE-D1-86?3??Port #3?Bridge(+)?192.168.72.104 (IPv4)|" +
	"Port 6?D0-69-9E-B5-B3-B8?9??Port #9?Bridge(+)?192.168.98.169 (IPv4)|"

func TestParseGen1Neighbours(t *testing.T) {
	got := parseGen1Neighbours(gen1Sample)
	want := []neighbour{
		{"1", "d0:69:9e:4b:73:cd", "eth1", "192.168.2.4", "LUM491425257"},
		{"2", "d0:69:9e:ae:d1:86", "3", "192.168.72.104", ""},
		{"6", "d0:69:9e:b5:b3:b8", "9", "192.168.98.169", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d neighbours, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("neighbour %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if n := parseGen1Neighbours(""); len(n) != 0 {
		t.Errorf("empty table gave %+v", n)
	}
}

func TestParseNodeNeighbours(t *testing.T) {
	body := `[{"port":"eth2","chassis_id":"d0:69:9e:a3:10:af","chassis_id_subtype":"mac","remote_port_id":"5","remote_port_id_subtype":"local","mgmt_addresses":["192.168.148.50"],"system_name":null,"system_description":"GigaCore10 (standalone) v3.1.5 2026-03-26"}]`
	got, err := parseNodeNeighbours(body)
	if err != nil {
		t.Fatal(err)
	}
	want := neighbour{"eth2", "d0:69:9e:a3:10:af", "5", "192.168.148.50", ""}
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %+v, want [%+v]", got, want)
	}
	if _, err := parseNodeNeighbours("<html>"); err == nil {
		t.Error("expected an error for a non-JSON reply")
	}
}

// Captured from a real LumiNode 12 (v2.9.1), 6 Nov 2026: the switch's port is port_id here, and its address is the one
// this computer had too, which is how the missing switch was found.
const nodeSample291 = `[{"port":"eth2","chassis_id_subtype":"MAC Address","chassis_id":"d0:69:9e:c8:b5:d3","remote_port_id_subtype":"Local","port_id":"0/9","mgmt_addresses":["192.168.1.56"],"system_name":"GigaCore 16tf-56","system_description":"GigaCore16tf, v1.9.2"}]`

func TestParseNodeNeighboursV291(t *testing.T) {
	got, err := parseNodeNeighbours(nodeSample291)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
	want := neighbour{"eth2", "d0:69:9e:c8:b5:d3", "0/9", "192.168.1.56", "GigaCore 16tf-56"}
	if got[0] != want {
		t.Errorf("got %+v, want %+v", got[0], want)
	}
}

func TestOwnAddressClashes(t *testing.T) {
	ifaces := []luminex.Iface{{Name: "en11", Address: "192.168.1.56", Prefix: 16}}
	devs := []luminex.Device{{Serial: "A", Name: "Switch", IP: "192.168.1.57"}}
	topo := &Topology{Links: []Link{{From: "N1", ToName: "Switch", ToIP: "192.168.1.57"}, {From: "N2", ToName: "GigaCore 16tf-56", ToIP: "192.168.1.56"}, {From: "N3", ToName: "GigaCore 16tf-56", ToIP: "192.168.1.56"}}}
	got := ownAddressClashes(devs, topo, ifaces)
	if len(got) != 1 || got[0] != (OwnClash{"GigaCore 16tf-56", "192.168.1.56", "en11"}) {
		t.Errorf("a neighbour's report should count once: %+v", got)
	}
	// A device in the list counts by itself, by serial if it has no name, and no topology is fine.
	got = ownAddressClashes([]luminex.Device{{Serial: "B", IP: "192.168.1.56"}}, nil, ifaces)
	if len(got) != 1 || got[0].Name != "B" {
		t.Errorf("listed device: %+v", got)
	}
	if got := ownAddressClashes(devs, nil, ifaces); len(got) != 0 {
		t.Errorf("no clash expected: %+v", got)
	}
}

// Captured from a real GigaCore 16tf (v1.9.2), 6 Nov 2026.
const gen2Sample = `[{"port_number":11,"chassis_id":"D0:69:9E:C8:B5:D3","chassis_id_subtype":"MAC Address","sys_name":"GigaCore 16tf-56","sys_description":"GigaCore16tf, v1.9.2","remote_port":"0/12","remote_port_id_subtype":"Local","remote_port_description":"Port 12","mgmt_address":"192.168.1.56","mgmt_address_subtype":"Interface Index","ttl":40,"source_address":"D0:69:9E:52:56:64"},{"port_number":9,"chassis_id":"D0:69:9E:7D:59:4E","chassis_id_subtype":"MAC Address","sys_name":"LUM645880237","sys_description":"LumiNode12, v2.9.1","remote_port":"D0:69:9E:42:0B:89","remote_port_id_subtype":"MAC Address","remote_port_description":"","mgmt_address":"192.168.1.250, fe80::1","mgmt_address_subtype":"Interface Index","ttl":121,"source_address":"D0:69:9E:42:0B:89"}]`

func TestParseGen2Neighbours(t *testing.T) {
	got, err := parseGen2Neighbours(gen2Sample)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %+v", err, got)
	}
	want := []neighbour{
		{"11", "d0:69:9e:c8:b5:d3", "0/12", "192.168.1.56", "GigaCore 16tf-56"},
		{"9", "d0:69:9e:7d:59:4e", "D0:69:9E:42:0B:89", "192.168.1.250", "LUM645880237"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("neighbour %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if n, err := parseGen2Neighbours("[]"); err != nil || len(n) != 0 {
		t.Errorf("empty table: %v %+v", err, n)
	}
	if _, err := parseGen2Neighbours("<html>"); err == nil {
		t.Error("expected an error for a non-JSON reply")
	}
}
