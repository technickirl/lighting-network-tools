package luminex

// TXT record → device description. No HTTP needed.
//
// NOTE: the GigaCore TXT fields have not yet been checked against a real
// Gen1 and Gen2 switch. The raw TXT is kept on every device and shown in the
// UI so the parser can be corrected from real output.

import (
	"regexp"
	"strconv"
	"strings"
)

// GigaCore first generation (HTML-only interface, /config/misc reset).
var gen1Re = regexp.MustCompile(`(?i)gigacore\s*-?\s*(10|12|14R|16XT|16RFO|26i)\b`)

// GigaCore second generation (REST API). Per support.luminex.be
// "GigaCore generation 2 devices": 10i 16i 30i 16t 18t 20t 10t 10t-IP 16tf O26-I S30-I.
var gen2Re = regexp.MustCompile(`(?i)gigacore\s*-?\s*(10i|10t(?:-?IP)?|16i|16t|16tf|18t|20t|30i|O26|S30)\b`)

var ipPrefixRe = regexp.MustCompile(`^(\d+\.\d+\.\d+\.\d+)(?:/(\d+))?`)

// Device is one row in the table. Field names match what public/app.js expects.
type Device struct {
	ID          string            `json:"id"`
	Instance    string            `json:"instance"`
	Host        string            `json:"host"`
	MdnsPort    int               `json:"mdnsPort"`
	IP          string            `json:"ip"`
	Prefix      int               `json:"prefix"`
	ProductType string            `json:"productType"`
	Family      string            `json:"family"`     // luminode | lumicore | gigacore | unknown
	Generation  string            `json:"generation"` // gen1 | gen2 | ""
	TypeLabel   string            `json:"typeLabel"`
	Model       string            `json:"model"`
	Name        string            `json:"name"`
	Serial      string            `json:"serial"`
	MAC         string            `json:"mac"`
	Firmware    string            `json:"firmware"`
	Txt         map[string]string `json:"txt"`
	RawTxt      string            `json:"rawTxt"`

	// Filled in by the server after a Scan.
	Reachable    bool   `json:"reachable"`
	ReachVia     string `json:"reachVia"`
	ReachReason  string `json:"reachReason"`
	ReachFix     string `json:"reachFix"`
	ReachWarning string `json:"reachWarning"`
	APIKind      string `json:"apiKind"`   // luminode | gen2 | gen1 | "" (cannot reset)
	Manual       bool   `json:"manual"`    // added by address, not found by a scan
	SharedIP     bool   `json:"sharedIp"`  // another device in the scan is on the same address
	LinkRoute    string `json:"linkRoute"` // URL host of its IPv6 link-local address, used while the address is shared
}

func first(kv map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := kv[k]; ok && v != "" {
			return v
		}
	}
	return ""
}

func ParseTxt(kv map[string]string) Device {
	d := Device{Txt: kv}
	if m := ipPrefixRe.FindStringSubmatch(first(kv, "ip_address", "ip")); m != nil {
		d.IP = m[1]
		// Only a real IPv4 prefix: anything else would make net.CIDRMask nil and
		// the subnet checks silently pass.
		if n, err := strconv.Atoi(m[2]); err == nil && n >= 1 && n <= 32 {
			d.Prefix = n
		}
	}
	d.ProductType = first(kv, "product_type", "product", "model")
	pt := strings.ToLower(d.ProductType)
	d.Family = "unknown"
	switch {
	case strings.Contains(pt, "luminode"):
		d.Family, d.Generation = "luminode", "gen2"
	case strings.Contains(pt, "lumicore"):
		d.Family, d.Generation = "lumicore", "gen2"
	case strings.Contains(pt, "gigacore"):
		d.Family = "gigacore"
		if gen1Re.MatchString(d.ProductType) {
			d.Generation = "gen1"
		} else if gen2Re.MatchString(d.ProductType) {
			d.Generation = "gen2"
		}
	}
	// Some firmware may advertise the generation directly; trust it if present.
	if g := strings.ToLower(strings.TrimSpace(kv["generation"])); g != "" {
		if strings.HasSuffix(g, "1") {
			d.Generation = "gen1"
		} else if strings.HasSuffix(g, "2") {
			d.Generation = "gen2"
		}
	}
	d.TypeLabel = TypeLabel(d.Family, d.Generation)
	d.Model = d.ProductType
	if d.Model == "" {
		d.Model = "(unknown)"
	}
	d.Name = first(kv, "device_name", "name")
	d.Serial = first(kv, "serial", "serial_number")
	d.MAC = strings.ToLower(first(kv, "mac", "mac_address"))
	d.Firmware = first(kv, "firmware_version", "firmware", "version")
	d.APIKind = APIKind(d)
	return d
}

func TypeLabel(family, generation string) string {
	switch family {
	case "luminode":
		return "LumiNode"
	case "lumicore":
		return "LumiCore"
	case "gigacore":
		switch generation {
		case "gen1":
			return "GigaCore Gen1"
		case "gen2":
			return "GigaCore Gen2"
		}
		return "GigaCore (gen ?)"
	}
	return "Unknown"
}

// APIKind says which HTTP interface the device has, or "" if it must not be reset.
func APIKind(d Device) string {
	switch {
	case d.Family == "luminode" || d.Family == "lumicore":
		return "luminode"
	case d.Family == "gigacore" && d.Generation == "gen2":
		return "gen2"
	case d.Family == "gigacore" && d.Generation == "gen1":
		return "gen1"
	}
	return ""
}

// parseDnsSdTxt parses the TXT line that `dns-sd -L` prints:
// key=value key=value ... with backslash escapes.
func parseDnsSdTxt(text string) map[string]string {
	kv := map[string]string{}
	re := regexp.MustCompile(`([A-Za-z0-9_.-]+)=((?:[^\s\\]|\\.)*)`)
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		v := regexp.MustCompile(`\\(.)`).ReplaceAllString(m[2], "$1")
		kv[m[1]] = v
	}
	return kv
}

// parseRawTxt parses raw TXT record strings ("key=value" entries).
func parseRawTxt(entries []string) map[string]string {
	kv := map[string]string{}
	for _, e := range entries {
		if i := strings.IndexByte(e, '='); i >= 0 {
			kv[e[:i]] = e[i+1:]
		} else if e != "" {
			kv[e] = ""
		}
	}
	return kv
}

// APIHost is the host for requests to this device: its IP, unless that is shared and it has an IPv6 route.
func (d Device) APIHost() string {
	if d.LinkRoute != "" {
		return d.LinkRoute
	}
	return d.IP
}
