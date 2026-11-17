package commission

import (
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"net"
	"strings"
	"testing"
)

func sharedFixture(t *testing.T) []luminex.Device {
	t.Helper()
	oldCheck := checkShared
	checkShared = func() bool { return true }
	oldState := state.devices
	t.Cleanup(func() { checkShared, state.devices = oldCheck, oldState })
	return []luminex.Device{
		{ID: "A", Serial: "A", IP: "192.168.1.30", Prefix: 16, APIKind: "luminode", Reachable: true},
		{ID: "B", Serial: "B", IP: "192.168.1.30", Prefix: 16, APIKind: "luminode", Reachable: true},
		{ID: "C", Serial: "C", IP: "192.168.1.30", Prefix: 16, APIKind: "gen1", Reachable: true},
		{ID: "D", Serial: "D", IP: "192.168.1.31", Prefix: 16, APIKind: "luminode", Reachable: true},
	}
}

func TestMarkShared(t *testing.T) {
	devs := sharedFixture(t)
	markShared(devs, map[string]string{"A": "[fe80::a%254]", "B": "[fe80::b%254]", "C": ""})
	if !devs[0].SharedIP || devs[0].APIHost() != "[fe80::a%254]" || !devs[0].Reachable {
		t.Errorf("a shared device with a route: %+v", devs[0])
	}
	if !devs[2].SharedIP || devs[2].Reachable || !strings.Contains(devs[2].ReachReason, "does not answer over IPv6") {
		t.Errorf("a shared device with no route: %+v", devs[2])
	}
	if devs[3].SharedIP || devs[3].APIHost() != "192.168.1.31" {
		t.Errorf("a device on its own: %+v", devs[3])
	}
	// One moves away and the other two stop sharing; the next device keeps its own address as the host.
	devs = devs[:2]
	devs[0].IP = "192.168.1.40"
	markShared(devs, nil)
	if devs[0].SharedIP || devs[0].LinkRoute != "" || devs[0].APIHost() != "192.168.1.40" {
		t.Errorf("no longer shared: %+v", devs[0])
	}
}

func TestSharedDeviceRequestsUseItsRoute(t *testing.T) {
	d := luminex.Device{IP: "192.168.1.30", APIKind: "luminode", LinkRoute: "[fe80::a%254]"}
	_, rq := ipRequests(d, IPPlan{IP: "192.168.12.1", Netmask: "255.255.0.0", Gateway: "0.0.0.0"})
	if !strings.Contains(rq.URL, "[fe80::a%254]") {
		t.Errorf("the IP change should go over IPv6: %s", rq.URL)
	}
	if p := kinds["luminode"].probe(d.APIHost()); !strings.Contains(p.URL, "[fe80::a%254]") {
		t.Errorf("the auth probe should go over IPv6: %s", p.URL)
	}
}

func TestSharedDeviceWithNoRouteIsRefused(t *testing.T) {
	devs := sharedFixture(t)
	markShared(devs, map[string]string{"A": "[fe80::a%254]", "C": ""})
	state.mu.Lock()
	state.devices = devs
	state.mu.Unlock()
	if _, msg := lookup([]string{"A", "D"}); msg != "" {
		t.Errorf("a device with a route, and one on its own, should be accepted: %q", msg)
	}
	if _, msg := lookup([]string{"C"}); !strings.Contains(msg, "Connect it on its own") {
		t.Errorf("a shared device with no route should be refused: %q", msg)
	}
	if locked, why := Locked("192.168.1.30"); !locked || why != sharedMsg {
		t.Errorf("Configure writes to a shared address should be locked: %v %q", locked, why)
	}
	if locked, _ := Locked("192.168.1.31"); locked {
		t.Error("an address only one device has should not be locked")
	}
}

func TestResolveRouteChecksTheSerial(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil || len(ifaces) == 0 {
		t.Skip("no interfaces here")
	}
	old := localIfaces
	localIfaces = func() []luminex.Iface { return []luminex.Iface{{Name: ifaces[0].Name}} }
	t.Cleanup(func() { localIfaces = old })
	f := &fakeNode{info: map[string]any{"serial": "LUM669433015"}}
	d := nodeFor(t, f)
	d.MAC = "d0:69:9e:7d:59:4e"
	route := resolveRoute(d)
	if !strings.HasPrefix(route, "[fe80::d269:9eff:fe7d:594e%25") {
		t.Errorf("expected a route: %q", route)
	}
	d.Serial = "SOMEONE ELSE"
	if route := resolveRoute(d); route != "" {
		t.Errorf("another serial should not count: %q", route)
	}
	d.APIKind = "gen1"
	if route := resolveRoute(d); route != "" {
		t.Errorf("Gen1 has no route: %q", route)
	}
}
