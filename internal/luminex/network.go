package luminex

// Local interface enumeration and Reachability maths (IPv4 only).

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Iface struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Netmask string `json:"netmask"`
	Prefix  int    `json:"prefix"`
	ifi     net.Interface
	ipnet   *net.IPNet
}

// LocalInterfaces returns every non-loopback IPv4 address on this machine.
func LocalInterfaces() []Iface {
	var out []Iface
	ifis, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifi := range ifis {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || ip4.IsLoopback() {
				continue
			}
			ones, _ := ipn.Mask.Size()
			out = append(out, Iface{
				Name: ifi.Name, Address: ip4.String(), Netmask: net.IP(ipn.Mask).String(), Prefix: ones,
				ifi: ifi, ipnet: &net.IPNet{IP: ip4, Mask: ipn.Mask},
			})
		}
	}
	return out
}

type Reach struct {
	Reachable bool
	Via       string
	Reason    string
	Fix       string
	Warning   string
}

func sameSubnet(a, b net.IP, prefix int) bool {
	m := net.CIDRMask(prefix, 32)
	return a.Mask(m).Equal(b.Mask(m))
}

// LocalOnly is the guard on a tool's localhost page, which has no login. Every request must name this computer in Host:
// a website that reaches localhost by a DNS trick (its own name, re-pointed at 127.0.0.1) gives itself away there, and
// without the check it could read the page's data, the other-devices PIN included. A write, and the live WebSocket,
// must also come from this tool's own page: browsers send Origin on those, so another website open in the same
// browser cannot send a reset or listen in. A plain read from another site is let through, as that site cannot see
// the answer.
func LocalOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		local := host == "localhost" || net.ParseIP(strings.Trim(host, "[]")).IsLoopback()
		refuse := !local
		if !refuse && (r.Method != http.MethodGet && r.Method != http.MethodHead || strings.EqualFold(r.Header.Get("Upgrade"), "websocket")) {
			refuse = !sameOrigin(r)
		}
		if refuse {
			Warnf("refused %s %s from Origin %q, Host %q", r.Method, r.URL.Path, r.Header.Get("Origin"), r.Host)
			http.Error(w, `{"error":"Refused: not from this tool's page."}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin: the request has no Origin (a script, not a browser page) or its Origin is the address it was sent to.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

// LinkLocalFromMAC is the IPv6 link-local address a device makes from its MAC (EUI-64), or "" if mac is not one:
// d0:69:9e:7d:59:4e gives fe80::d269:9eff:fe7d:594e.
func LinkLocalFromMAC(mac string) string {
	m, err := net.ParseMAC(mac)
	if err != nil || len(m) != 6 {
		return ""
	}
	ip := net.IP{0xfe, 0x80, 0, 0, 0, 0, 0, 0, m[0] ^ 2, m[1], m[2], 0xff, 0xfe, m[3], m[4], m[5]}
	return ip.String()
}

// OwnAddress returns the interface of ifaces that has ip as its own address, or "". A device on the same address as
// this computer cannot be reached: anything sent to that address stays on this computer.
func OwnAddress(ip string, ifaces []Iface) string {
	for _, i := range ifaces {
		if i.Address == ip {
			return i.Name
		}
	}
	return ""
}

// Reachability: is ip inside any local interface's subnet?
func Reachability(ip string, devicePrefix int) Reach {
	dip := net.ParseIP(ip)
	if dip == nil || dip.To4() == nil {
		return Reach{Reason: "No valid IP address reported.",
			Fix: "Rescan, or check the front panel."}
	}
	dip = dip.To4()
	// The test fakes answer on this computer's own address, on DEVICE_PORT; real devices never should.
	if name := OwnAddress(ip, LocalInterfaces()); name != "" && os.Getenv("DEVICE_PORT") == "" {
		return Reach{Reason: fmt.Sprintf("On %s, the same address as this computer (%s).", ip, name),
			Fix: "Change this computer's address, then rescan."}
	}
	for _, i := range LocalInterfaces() {
		if i.ipnet.Contains(dip) {
			r := Reach{Reachable: true, Via: fmt.Sprintf("%s %s/%d", i.Name, i.Address, i.Prefix)}
			// The device's own mask must also include us, or it cannot reply.
			if devicePrefix > 0 && !sameSubnet(dip, net.ParseIP(i.Address).To4(), devicePrefix) {
				r.Warning = fmt.Sprintf("Its /%d subnet excludes this computer (%s); it may not reply.", devicePrefix, i.Address)
			}
			return r
		}
	}
	// dip is the parsed 4-byte address, so this also holds for an IPv4-mapped
	// literal ("::ffff:192.0.2.1"), which has no dots to split on.
	suggested := fmt.Sprintf("%d.%d.%d.250 / 255.255.255.0", dip[0], dip[1], dip[2])
	if dip[0] == 2 {
		suggested = "2.0.0.250 / 255.0.0.0"
	}
	return Reach{
		Reason: fmt.Sprintf("No adapter on %s's subnet.", ip),
		Fix:    fmt.Sprintf("Add %s to the adapter, then rescan.", suggested),
	}
}

// LostInterfaces returns the entries of previous (name + address) that are
// missing from current. macOS strips a link-down interface of its IPv4
// addresses, manual ones included, so when the cable to a rebooting device
// or switch drops, the tool's own LAN address vanishes and every device on
// that subnet turns "unreachable" until the link is back. The UI uses this
// to say "link down" instead.
func LostInterfaces(previous, current []Iface) []Iface {
	have := map[string]bool{}
	for _, c := range current {
		have[c.Name+" "+c.Address] = true
	}
	var lost []Iface
	for _, p := range previous {
		if !have[p.Name+" "+p.Address] {
			lost = append(lost, p)
		}
	}
	return lost
}

// WatchInterfaces reads this computer's addresses every interval and calls
// onChange when one has appeared or gone: a USB network card plugged in, a
// cable connected, an address typed into the adapter's settings. It blocks
// until stop closes (nil never does), so run it with go.
func WatchInterfaces(stop <-chan struct{}, read func() []Iface, every time.Duration, onChange func(added, removed []Iface)) {
	previous := read()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		current := read()
		// Unlike LostInterfaces this counts a changed netmask too: it decides which devices are in reach.
		missing := func(from, in []Iface) (out []Iface) {
			have := map[string]bool{}
			for _, i := range in {
				have[fmt.Sprintf("%s %s/%d", i.Name, i.Address, i.Prefix)] = true
			}
			for _, i := range from {
				if !have[fmt.Sprintf("%s %s/%d", i.Name, i.Address, i.Prefix)] {
					out = append(out, i)
				}
			}
			return out
		}
		added, removed := missing(current, previous), missing(previous, current)
		previous = current
		if len(added)+len(removed) > 0 {
			onChange(added, removed)
		}
	}
}

// DescribeInterfaces lists addresses for a log line: "en7 192.168.1.250/16, en0 10.0.0.4/24".
func DescribeInterfaces(ifaces []Iface) string {
	parts := make([]string, len(ifaces))
	for n, i := range ifaces {
		parts[n] = fmt.Sprintf("%s %s/%d", i.Name, i.Address, i.Prefix)
	}
	return strings.Join(parts, ", ")
}
