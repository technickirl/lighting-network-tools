package commission

// Devices that share an IP address. LumiNodes usually leave a hire company all on one address, and a request to that
// address reaches only the device this computer's ARP cache points at, so the tool cannot choose which one it talks to.
//
// Each device also has an IPv6 link-local address made from its own MAC, so a request sent there can only reach that
// device, with no admin rights and no temporary addresses. LumiNode 12 (v2.9.1) and GigaCore 16tf (v1.9.2) answer on it;
// a GigaCore 12 Gen1 (v3.1.4) does not, so a shared device with no such route is refused until it is connected on its own.
//
// IPv4 stays the normal route: it is no faster, and every device has it. The IPv6 route is only used while an address is
// shared, and goes away with the next scan that finds the address unshared.

import (
	"encoding/json"
	"fmt"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// The test fakes all answer on this computer's address, so they would always look as if they shared one.
var checkShared = func() bool { return os.Getenv("DEVICE_PORT") == "" }

// findRoute is a variable so tests can stand in for the network.
var findRoute = resolveRoute

const sharedMsg = "Address shared: a change could reach the wrong device. Fix that first."

func sharedCounts(devs []luminex.Device) map[string]int {
	n := map[string]int{}
	for _, d := range devs {
		if d.IP != "" {
			n[d.IP]++
		}
	}
	return n
}

// resolveRoute finds d's IPv6 link-local address, on whichever of this computer's adapters it answers, as a host for a
// URL. It returns "" if there is none. A refusal counts, as the address is made from the device's own MAC; a reply naming
// another serial does not.
func resolveRoute(d luminex.Device) string {
	path := map[string]string{"luminode": "/api/deviceinfo", "gen2": "/api/device"}[d.APIKind]
	ll := luminex.LinkLocalFromMAC(d.MAC)
	if path == "" || ll == "" {
		return ""
	}
	tried := map[string]bool{}
	for _, i := range localIfaces() {
		if tried[i.Name] {
			continue
		}
		tried[i.Name] = true
		iface, err := net.InterfaceByName(i.Name)
		if err != nil {
			continue
		}
		host := fmt.Sprintf("[%s%%25%d]", ll, iface.Index)
		r, err := deviceFetch("GET", base(host)+path, "", "", nil, 2500*time.Millisecond)
		if err != nil {
			continue
		}
		if luminex.Denied(r.Status) {
			return host
		}
		var got struct {
			Serial string `json:"serial"`
		}
		if r.Status == http.StatusOK && json.Unmarshal([]byte(r.Text), &got) == nil && strings.EqualFold(got.Serial, d.Serial) {
			return host
		}
	}
	return ""
}

// findRoutes looks up a route for every device that shares its address.
func findRoutes(devs []luminex.Device) map[string]string {
	counts := sharedCounts(devs)
	routes := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, d := range devs {
		if counts[d.IP] < 2 {
			continue
		}
		wg.Add(1)
		go func(d luminex.Device) {
			defer wg.Done()
			route := findRoute(d)
			luminex.Warnf("[%s] shares %s with another device; IPv6 route: %q", d.Serial, d.IP, route)
			mu.Lock()
			routes[d.ID] = route
			mu.Unlock()
		}(d)
	}
	wg.Wait()
	return routes
}

// markShared flags the devices that share an address and how each can be reached. No network: routes come from findRoutes.
func markShared(devs []luminex.Device, routes map[string]string) {
	if !checkShared() {
		return
	}
	counts := sharedCounts(devs)
	for i := range devs {
		d := &devs[i]
		if counts[d.IP] < 2 {
			if d.SharedIP { // it no longer shares: back to the normal route
				*d = enrich(*d)
				d.SharedIP, d.LinkRoute = false, ""
			}
			continue
		}
		d.SharedIP, d.LinkRoute = true, routes[d.ID]
		if d.LinkRoute != "" {
			d.Reachable, d.ReachVia, d.ReachReason, d.ReachFix, d.ReachWarning = true, "IPv6 link-local", "", "", ""
		} else {
			d.Reachable, d.ReachVia, d.ReachWarning = false, "", ""
			d.ReachReason = fmt.Sprintf("It shares %s and does not answer over IPv6.", d.IP)
			d.ReachFix = "Connect it on its own, then Scan."
		}
	}
}
