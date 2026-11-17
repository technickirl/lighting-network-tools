package commission

// Topology: who is plugged into what, read from each device's LLDP neighbour
// table. Combo mode uses it to pair every node with the switch it hangs off and
// to walk the chain of switches.
//
//   LumiNode / LumiCore   GET /api/lldp/neighbor  → JSON array, one entry per neighbour:
//                         {"port":"eth2","chassis_id":"d0:69:9e:a3:10:af","remote_port_id":"5",
//                          "mgmt_addresses":["192.168.148.50"],"system_description":"GigaCore10 …"}
//                         (read off real LumiNode 4s and a 12 on v2.8.1, 4 Nov 2026)
//   GigaCore Gen1         GET /config/lldp_neighbors  → text, neighbours split by "|", fields by "?":
//                         "Port 5?D0-69-9E-1A-3F-2B?eth2?LUM326164803?eth2?Other(-), Bridge(+)?192.168.2.2 (IPv4)…|"
//                         (read off real GigaCore 10/12 v3.1.5, 4 Nov 2026)
//   GigaCore Gen2         GET /api/lldp/neighbor  → JSON array, one entry per neighbour:
//                         {"port_number":11,"chassis_id":"D0:69:9E:C8:B5:D3","sys_name":"GigaCore 16tf-56",
//                          "remote_port":"0/12","mgmt_address":"192.168.1.56", ...}
//                         (read off a real GigaCore 16tf v1.9.2, 6 Nov 2026)
//
// Reads only; DRY_RUN does not affect them.

import (
	"encoding/json"
	"fmt"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Link is one neighbour entry as reported by the device named in From.
type Link struct {
	From     string `json:"from"`     // serial of the device that reported it
	FromPort string `json:"fromPort"` // that device's own port ("5", "eth2")
	ToMAC    string `json:"toMac"`
	To       string `json:"to"`     // serial of the neighbour, "" if it is not in the scan
	ToPort   string `json:"toPort"` // the neighbour's port, as the neighbour announces it
	ToIP     string `json:"toIp"`   // the neighbour's management address, as it announces it
	ToName   string `json:"toName"` // the neighbour's name, as it announces it
}

type Topology struct {
	At     int64    `json:"at"`
	Links  []Link   `json:"links"`
	Unread []string `json:"unread"` // devices whose neighbour table could not be read, with the reason
}

func normMAC(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ":"))
}

type neighbour struct{ fromPort, mac, toPort, ip, name string }

var firstIPv4 = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+`)

// parseGen1Neighbours reads the /config/lldp_neighbors text.
func parseGen1Neighbours(text string) []neighbour {
	var out []neighbour
	for _, rec := range strings.Split(text, "|") {
		f := strings.Split(rec, "?")
		if len(f) < 3 || !strings.HasPrefix(f[0], "Port ") {
			continue
		}
		n := neighbour{fromPort: strings.TrimPrefix(f[0], "Port "), mac: normMAC(f[1]), toPort: f[2]}
		if len(f) > 3 {
			n.name = f[3]
		}
		if len(f) > 6 {
			n.ip = firstIPv4.FindString(f[6])
		}
		out = append(out, n)
	}
	return out
}

func parseNodeNeighbours(body string) ([]neighbour, error) {
	var raw []struct {
		Port         string   `json:"port"`
		ChassisID    string   `json:"chassis_id"`
		RemotePortID string   `json:"remote_port_id"` // firmware v2.8.1
		PortID       string   `json:"port_id"`        // firmware v2.9.1
		Mgmt         []string `json:"mgmt_addresses"`
		SystemName   string   `json:"system_name"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, fmt.Errorf("unexpected reply: %v", err)
	}
	var out []neighbour
	for _, r := range raw {
		n := neighbour{fromPort: r.Port, mac: normMAC(r.ChassisID), toPort: r.RemotePortID, name: r.SystemName}
		if n.toPort == "" {
			n.toPort = r.PortID
		}
		for _, a := range r.Mgmt {
			if firstIPv4.MatchString(a) {
				n.ip = a
				break
			}
		}
		out = append(out, n)
	}
	return out, nil
}

// parseGen2Neighbours reads a GigaCore Gen2's /api/lldp/neighbor. mgmt_address may be comma separated.
func parseGen2Neighbours(body string) ([]neighbour, error) {
	var raw []struct {
		Port       int    `json:"port_number"`
		ChassisID  string `json:"chassis_id"`
		RemotePort string `json:"remote_port"`
		Mgmt       string `json:"mgmt_address"`
		SysName    string `json:"sys_name"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, fmt.Errorf("unexpected reply: %v", err)
	}
	var out []neighbour
	for _, r := range raw {
		out = append(out, neighbour{fromPort: fmt.Sprint(r.Port), mac: normMAC(r.ChassisID), toPort: r.RemotePort, ip: firstIPv4.FindString(strings.TrimSpace(r.Mgmt)), name: r.SysName})
	}
	return out, nil
}

func readNeighbours(d luminex.Device) ([]neighbour, error) {
	auth, needsPw, err := findAuth(d, nil)
	if err != nil {
		return nil, err
	}
	if needsPw {
		return nil, fmt.Errorf("needs a password")
	}
	path := "/api/lldp/neighbor"
	if d.APIKind == "gen1" {
		path = "/config/lldp_neighbors"
	}
	r, err := deviceFetch("GET", base(d.APIHost())+path, "", "", auth, 6*time.Second)
	if err != nil {
		return nil, err
	}
	if r.Status != 200 {
		return nil, fmt.Errorf("device answered HTTP %d", r.Status)
	}
	switch d.APIKind {
	case "gen1":
		return parseGen1Neighbours(r.Text), nil
	case "gen2":
		return parseGen2Neighbours(r.Text)
	}
	return parseNodeNeighbours(r.Text)
}

// readTopology asks every reachable device for its neighbours and stores the result.
func readTopology() {
	state.mu.Lock()
	if state.readingTopology {
		state.mu.Unlock()
		return
	}
	state.readingTopology = true
	devs := append([]luminex.Device(nil), state.devices...)
	state.mu.Unlock()
	notify()

	bySerial := map[string]string{} // MAC → serial
	for _, d := range devs {
		if d.MAC != "" && d.Serial != "" {
			bySerial[normMAC(d.MAC)] = d.Serial
		}
	}

	var mu sync.Mutex
	topo := &Topology{Links: []Link{}, Unread: []string{}}
	var wg sync.WaitGroup
	for _, d := range devs {
		if !d.Reachable || d.APIKind == "" || d.Serial == "" {
			continue
		}
		wg.Add(1)
		go func(d luminex.Device) {
			defer wg.Done()
			ns, err := readNeighbours(d)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				luminex.Warnf("[%s] could not read neighbours: %v", d.Serial, err)
				topo.Unread = append(topo.Unread, fmt.Sprintf("%s (%s)", d.Serial, err))
				return
			}
			for _, n := range ns {
				topo.Links = append(topo.Links, Link{From: d.Serial, FromPort: n.fromPort, ToMAC: n.mac, To: bySerial[n.mac], ToPort: n.toPort, ToIP: n.ip, ToName: n.name})
			}
		}(d)
	}
	wg.Wait()
	topo.At = nowMs()
	luminex.Logf("topology: %d link(s) from %d device(s), %d unread", len(topo.Links), len(devs), len(topo.Unread))

	state.mu.Lock()
	state.topology, state.readingTopology = topo, false
	state.mu.Unlock()
	notify()
}

// OwnClash is a device on the same address as this computer: it is in the list, or a neighbour reports it.
type OwnClash struct {
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Interface string `json:"interface"`
}

// ownAddressClashes finds devices on one of this computer's own addresses. A device like that may not be in the scan at
// all, so the neighbours' reports count too.
func ownAddressClashes(devs []luminex.Device, topo *Topology, ifaces []luminex.Iface) []OwnClash {
	out := []OwnClash{}
	seen := map[string]bool{}
	add := func(name, ip string) {
		iface := luminex.OwnAddress(ip, ifaces)
		if iface == "" || seen[ip] {
			return
		}
		seen[ip] = true
		out = append(out, OwnClash{Name: name, IP: ip, Interface: iface})
	}
	for _, d := range devs {
		name := d.Name
		if name == "" {
			name = d.Serial
		}
		add(name, d.IP)
	}
	if topo != nil {
		for _, l := range topo.Links {
			add(l.ToName, l.ToIP)
		}
	}
	return out
}
