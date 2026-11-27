package commission

// Per-device HTTP calls: auth probe, IP configuration, factory reset, is-up.
//
// IP configuration (from iptop2, verified there on real devices in June 2026):
//   LumiNode / LumiCore        PUT /api/ipsettings {"ipaddress","netmask","gateway"}  → 200/202, device reboots
//   GigaCore Gen2              PUT /api/ip_settings {"mode":"static","ip_address","prefix_length","default_gateway"}
//                              (GigaCore WebApi 1.5.0 /api/doc, read off a real GigaCore 16tf v1.9.2 on 4 Nov 2026;
//                              the LumiNode path gives 404 "Field with name 'ipsettings' not found" on a switch)
//   GigaCore Gen1              GET /config/ip2_ll_ip (preflight, as the web UI does), then
//                              POST /config/ip2_config form-encoded (if_ct, if_vid_0, if_addr_0, if_mask_0 (CIDR),
//                              if_dhcp_0, rt_ct, rt_del_0, rt_net_0, rt_mask_0, rt_dest_0, rt_net_1, rt_mask_1, rt_dest_1)
// Device name (a settings run, settings.go, after the addresses are confirmed):
//   LumiNode / LumiCore        GET /api/deviceinfo (for ID and colors), then PUT /api/deviceinfo
//                              {"ID","short_name" (max 17),"long_name" (max 63),"colors"}  (WebAPI v2.7.2 / v2.9.1)
//   GigaCore Gen2              PUT /api/device/name "<name>"  (GigaCore WebApi 1.5.0; pattern [a-zA-Z0-9 _-], max 64)
//   GigaCore Gen1              no known call — the name step is skipped and the row says so.
// Factory reset: the calls are listed in docs/device-list.md.
//
// DRY_RUN=1 logs the exact write request instead of sending it. Probes (GET) still run.

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"net"
	"net/url"
	"strings"
	"time"
)

// Auth is the shared client's credentials type.
type Auth = luminex.Auth

func deviceFetch(method, url, contentType, body string, auth *Auth, timeout time.Duration) (*luminex.Response, error) {
	return luminex.Fetch(luminex.Call{Method: method, URL: url, ContentType: contentType, Body: body, Auth: auth, Timeout: timeout})
}

type request struct {
	Method, URL, ContentType, Body string
}

func base(ip string) string { return luminex.DeviceURL(ip, "") }

type kindDef struct {
	probe        func(ip string) request
	defaultAuths []*Auth
	verified     bool // reset call verified on a real device
}

var kinds = map[string]kindDef{
	"luminode": {probe: func(ip string) request { return request{"GET", base(ip) + "/api/deviceinfo", "", ""} },
		defaultAuths: []*Auth{nil, {Username: "admin"}}, verified: true},
	// Reset verified on a real GigaCore 16tf (v1.9.2) on 4 Nov 2026; its /api/doc
	// documents PUT /api/reset {keep_ip,keep_profiles,keep_tracing,keep_config,auto_reboot,wait}.
	"gen2": {probe: func(ip string) request { return request{"GET", base(ip) + "/api/device", "", ""} },
		defaultAuths: []*Auth{nil, {Username: "admin"}}, verified: true},
	// Gen1 rejects requests with no Authorization header at all.
	"gen1": {probe: func(ip string) request { return request{"GET", base(ip) + "/stat/ports", "", ""} },
		defaultAuths: []*Auth{{Username: "admin"}}, verified: true},
}

// IPPlan is what to write to one device.
type IPPlan struct {
	IP      string // new address
	Netmask string // dotted
	Gateway string // dotted, 0.0.0.0 for none
}

func maskToCIDR(mask string) int {
	ip := net.ParseIP(mask)
	if ip == nil || ip.To4() == nil {
		return 16
	}
	ones, _ := net.IPMask(ip.To4()).Size()
	return ones
}

// ipRequests returns the write request(s) that set a device's IP. Gen1 needs a
// harmless preflight GET first (the web UI does the same).
func ipRequests(d luminex.Device, p IPPlan) (preflight *request, write request) {
	switch d.APIKind {
	case "gen1":
		pre := request{"GET", base(d.APIHost()) + "/config/ip2_ll_ip", "", ""}
		// Field order matches the Gen1 web UI (iptop2 copied it); embedded parsers
		// can be order-sensitive, so do not let url.Values sort them.
		fields := [][2]string{
			{"if_ct", "1"}, {"if_vid_0", "1"}, {"if_addr_0", p.IP}, {"if_mask_0", fmt.Sprint(maskToCIDR(p.Netmask))}, {"if_dhcp_0", "0"},
			{"rt_ct", "2"}, {"rt_del_0", "1"}, {"rt_net_0", "0.0.0.0"}, {"rt_mask_0", "0"}, {"rt_dest_0", "0.0.0.0"},
			{"rt_net_1", "0.0.0.0"}, {"rt_mask_1", "0"}, {"rt_dest_1", p.Gateway},
		}
		parts := make([]string, 0, len(fields))
		for _, f := range fields {
			parts = append(parts, url.QueryEscape(f[0])+"="+url.QueryEscape(f[1]))
		}
		form := strings.Join(parts, "&")
		return &pre, request{"POST", base(d.APIHost()) + "/config/ip2_config", "application/x-www-form-urlencoded", form}
	case "gen2":
		// Partial PUT: the switch keeps the fields we leave out (fallback_timeout,
		// link_local_range, vlan, advanced). mode must be "static" or the address
		// is only a DHCP fallback.
		body := fmt.Sprintf(`{"mode":"static","ip_address":%q,"prefix_length":%d,"default_gateway":%q}`,
			p.IP, maskToCIDR(p.Netmask), p.Gateway)
		return nil, request{"PUT", base(d.APIHost()) + "/api/ip_settings", "application/json", body}
	default:
		body := fmt.Sprintf(`{"ipaddress":%q,"netmask":%q,"gateway":%q}`, p.IP, p.Netmask, p.Gateway)
		return nil, request{"PUT", base(d.APIHost()) + "/api/ipsettings", "application/json", body}
	}
}

// nameRequest builds the request that renames a device. ok is false when the
// device kind has no known name call. A LumiNode needs its current ID and
// colours first (the PUT replaces the whole object), so this does a GET.
func nameRequest(d luminex.Device, auth *Auth, name string) (rq request, ok bool, err error) {
	switch d.APIKind {
	case "luminode":
		r, err := deviceFetch("GET", base(d.APIHost())+"/api/deviceinfo", "", "", auth, 6*time.Second)
		if err != nil {
			return request{}, true, err
		}
		if r.Status < 200 || r.Status >= 300 {
			return request{}, true, fmt.Errorf("device answered HTTP %d reading deviceinfo", r.Status)
		}
		var info struct {
			ID     json.RawMessage `json:"ID"`
			Colors json.RawMessage `json:"colors"`
		}
		if err := json.Unmarshal([]byte(r.Text), &info); err != nil {
			return request{}, true, fmt.Errorf("deviceinfo was not JSON: %s", err)
		}
		id, colors := string(info.ID), string(info.Colors)
		if id == "" || id == "null" {
			id = "0"
		}
		if colors == "" || colors == "null" {
			colors = "[]"
		}
		short := name
		if len(short) > 17 { // short_name maxLength 17; it is what mDNS advertises
			short = strings.TrimSpace(short[:17])
		}
		if len(name) > 63 {
			name = name[:63]
		}
		body := fmt.Sprintf(`{"ID":%s,"short_name":%q,"long_name":%q,"colors":%s}`, id, short, name, colors)
		return request{"PUT", base(d.APIHost()) + "/api/deviceinfo", "application/json", body}, true, nil
	case "gen2":
		if len(name) > 64 {
			name = name[:64]
		}
		return request{"PUT", base(d.APIHost()) + "/api/device/name", "application/json", fmt.Sprintf("%q", name)}, true, nil
	}
	return request{}, false, nil
}

// resetRequest returns the factory-reset request. keepProfiles keeps user/DMX
// profiles; keepIP keeps the device's IP settings. Gen1's keep-IP form
// (factory=yes instead of factory_full=yes) is from the Companion module's
// gen1.ts and has not been sent to a real Gen1 switch yet.
func resetRequest(d luminex.Device, keepProfiles, keepIP bool) request {
	switch d.APIKind {
	case "gen1":
		body := "factory_full=yes"
		if keepIP {
			body = "factory=yes"
		}
		if !keepProfiles {
			body += "&clear_profiles=yes"
		}
		return request{"POST", base(d.APIHost()) + "/config/misc", "application/x-www-form-urlencoded", body}
	case "gen2":
		return request{"PUT", base(d.APIHost()) + "/api/reset", "application/json",
			fmt.Sprintf(`{"keep_ip":%t,"keep_profiles":%t,"wait":0}`, keepIP, keepProfiles)}
	default:
		return request{"POST", base(d.APIHost()) + "/api/reset", "application/json",
			fmt.Sprintf(`{"keep_ip_settings":%t,"keep_user_profiles":%t}`, keepIP, keepProfiles)}
	}
}

// findAuth finds credentials the device accepts without changing anything, and checks that the device answering at
// the address is the one meant (errWrongDevice if not).
func findAuth(d luminex.Device, userAuth *Auth) (*Auth, bool, error) {
	def := kinds[d.APIKind]
	candidates := def.defaultAuths
	if k := luminex.KnownAuth(d.IP); k != nil {
		candidates = append([]*Auth{k}, candidates...) // already accepted here, by the configure page or an earlier job
	}
	if userAuth != nil {
		candidates = []*Auth{userAuth}
	}
	for _, a := range candidates {
		p := def.probe(d.APIHost())
		r, err := deviceFetch(p.Method, p.URL, "", "", a, 6*time.Second)
		if err != nil {
			return nil, false, err
		}
		if r.Status == 401 || r.Status == 403 {
			continue
		}
		if err := checkIdentity(d, r, a); err != nil {
			return nil, false, err
		}
		return a, false, nil
	}
	return nil, true, nil
}

// errWrongDevice: the address answered, but as another device. Nothing may be written there.
var errWrongDevice = errors.New("another device answered")

// checkIdentity compares who answered at d's address with d. The list can be out of date, and two devices on one
// address are only known to share it when a scan heard both, so a write to an address could reach a device the
// operator did not pick. It refuses only on proof: the answer names a serial that is not d's, and does not name
// d's MAC. An answer that names neither is let through, as no firmware is known to leave them out on purpose.
//
// LumiNode /api/deviceinfo and Gen2 /api/device carry the serial in the probe's own reply. Gen1's probe does not, so
// its /config/switchlegend is read as well ("name,descr,id,serial,board_rev,board_ass,MAC", seen on v3.1.5).
func checkIdentity(d luminex.Device, probe *luminex.Response, auth *Auth) error {
	if d.Serial == "" {
		return nil
	}
	var serial, mac string
	switch d.APIKind {
	case "gen1":
		r, err := deviceFetch("GET", base(d.APIHost())+"/config/switchlegend", "", "", auth, 6*time.Second)
		if err != nil || r.Status != 200 {
			return nil
		}
		f := strings.Split(strings.TrimSpace(r.Text), ",")
		if len(f) < 7 {
			return nil
		}
		serial, mac = f[3], f[len(f)-1]
	default:
		var got struct {
			Serial string `json:"serial"`
			MAC    string `json:"mac_address"`
		}
		if probe.Status != 200 || json.Unmarshal([]byte(probe.Text), &got) != nil {
			return nil
		}
		serial, mac = got.Serial, got.MAC
	}
	serial = strings.TrimSpace(serial)
	if serial == "" || strings.EqualFold(serial, d.Serial) {
		return nil
	}
	if d.MAC != "" && normMAC(mac) == normMAC(d.MAC) {
		return nil
	}
	luminex.Warnf("[%s] %s answered as serial %s: refusing to write there", d.ID, d.IP, serial)
	return fmt.Errorf("%w at %s (serial %s, not %s). Nothing was sent. Scan again", errWrongDevice, d.IP, serial, d.Serial)
}

// noAnswerText is what a row says when findAuth fails.
func noAnswerText(d luminex.Device, err error) string {
	if errors.Is(err, errWrongDevice) {
		return strings.ToUpper(err.Error()[:1]) + err.Error()[1:] + "."
	}
	return fmt.Sprintf("No answer from %s (%s). Check cable and power.", d.IP, err)
}

type writeResult struct {
	Outcome string // sent | dropped | auth | dry | failed
	Reason  string
	Status  int
	Request string
}

// sendWrite performs a write request (IP change or reset). A request that never reached the device (no connection)
// has failed. One that was sent and then lost its connection is "dropped": the device may have acted on it and be
// restarting, or not, so the caller has to see it go down before believing it.
func sendWrite(d luminex.Device, pre *request, rq request, auth *Auth) writeResult {
	if luminex.DryRun {
		hdr := fmt.Sprintf(`{"Content-Type":%q`, rq.ContentType)
		if auth != nil {
			hdr += fmt.Sprintf(`,"Authorization":"Basic base64(%s:%s)"`, auth.Username, strings.Repeat("*", min(len(auth.Password), 3)))
		}
		hdr += "}"
		if pre != nil {
			luminex.Logf("DRY RUN — would first send: %s %s", pre.Method, pre.URL)
		}
		luminex.Logf("DRY RUN — would send: %s %s\n    headers: %s\n    body: %s", rq.Method, rq.URL, hdr, rq.Body)
		return writeResult{Outcome: "dry", Request: fmt.Sprintf("%s %s  %s", rq.Method, rq.URL, rq.Body)}
	}
	if pre != nil {
		if _, err := deviceFetch(pre.Method, pre.URL, "", "", auth, 6*time.Second); err != nil {
			luminex.Warnf("preflight %s failed (%s) — continuing anyway", pre.URL, err)
		}
	}
	r, err := deviceFetch(rq.Method, rq.URL, rq.ContentType, rq.Body, auth, 8*time.Second)
	if err != nil {
		if luminex.NotSent(err) {
			return writeResult{Outcome: "failed", Reason: "Not sent: " + err.Error() + ". Nothing was changed."}
		}
		return writeResult{Outcome: "dropped", Reason: err.Error()}
	}
	if d.APIKind == "gen1" && r.Status == 404 && strings.Contains(rq.URL, "/config/misc") {
		alt := strings.Replace(rq.URL, "/config/misc", "/confg/misc", 1)
		luminex.Warnf("Gen1 %s: /config/misc gave 404, trying %s", d.IP, alt)
		if r, err = deviceFetch(rq.Method, alt, rq.ContentType, rq.Body, auth, 8*time.Second); err != nil {
			if luminex.NotSent(err) {
				return writeResult{Outcome: "failed", Reason: "Not sent: " + err.Error() + ". Nothing was changed."}
			}
			return writeResult{Outcome: "dropped", Reason: err.Error()}
		}
	}
	switch {
	case r.Status == 401 || r.Status == 403:
		return writeResult{Outcome: "auth"}
	case r.Status >= 200 && r.Status < 300:
		return writeResult{Outcome: "sent", Status: r.Status}
	}
	reason := fmt.Sprintf("device answered HTTP %d", r.Status)
	if t := strings.TrimSpace(r.Text); t != "" {
		if len(t) > 120 {
			t = t[:120]
		}
		reason += ": " + t
	}
	return writeResult{Outcome: "failed", Reason: reason}
}

func isUp(d luminex.Device, auth *Auth) bool {
	def, ok := kinds[d.APIKind]
	if !ok {
		return false
	}
	p := def.probe(d.APIHost())
	_, err := deviceFetch(p.Method, p.URL, "", "", auth, 2500*time.Millisecond)
	return err == nil
}
