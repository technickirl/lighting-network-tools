package commission

// GigaCore Gen1 "groups" (what other switches call VLANs): reading them, naming them and applying a profile.
//
//	GET /config/groups  →  "nr/type/name/ports/taggedPorts/colour/aggregated/vid/snooping/querier/?/tagNative|…"
//	                       type is ISL, Management or Normal; ports are comma lists; name is URL-escaped; colour is "#RRGGBB".
//	GET /config/ports   →  one "|"-separated record per port, used here only to count the ports.
//
//	POST /config/groups with, for each changed group N: grp_name_N, grp_ports_N, grp_tports_N (only if any), grp_vid_N,
//	                       grp_snooping_N, grp_querier_N (the web page sends all of them, so this does too), then
//	                       POST /config/icfg_conf_save save=1. Only Normal groups (2 and up) are renamed here.
//
// Read off the web UI of a real GigaCore 10 v3.1.5 on 6 Nov 2026 (page_groups.js and switch_data.js). Gen2 has
// /api/vlans in its spec, which is not read here yet. Reading (readSwitchGroups) only sends GET requests; the
// writes are built by groupNameSteps and groupProfileSteps and go out through the settings run, which honours DRY_RUN.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

// GroupInfo is one group on one switch.
type GroupInfo struct {
	Nr         int    `json:"nr"`
	Type       string `json:"type"` // ISL | Management | Normal
	Name       string `json:"name"`
	Ports      []int  `json:"ports"`  // untagged members
	Tagged     []int  `json:"tagged"` // tagged members
	Color      string `json:"color"`
	VID        int    `json:"vid"`
	Aggregated bool   `json:"aggregated,omitempty"`
	Snooping   bool   `json:"snooping"`
	Querier    bool   `json:"querier"`
	TagNative  bool   `json:"tagNative,omitempty"` // ISL group only: tag the native VLAN
}

// SwitchGroups is what one switch reports.
type SwitchGroups struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	IP        string      `json:"ip"`
	Serial    string      `json:"serial"`
	Model     string      `json:"model"`
	PortCount int         `json:"portCount"`
	Groups    []GroupInfo `json:"groups"`
	Error     string      `json:"error,omitempty"`
}

func parsePortList(s string) []int {
	out := []int{} // an empty list, not null, in the JSON
	for _, f := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// parseGen1Groups reads the /config/groups text.
func parseGen1Groups(txt string) ([]GroupInfo, error) {
	var out []GroupInfo
	for _, rec := range strings.Split(strings.TrimSpace(txt), "|") {
		f := strings.Split(rec, "/")
		if len(f) < 9 { // the switch's own page skips records this short too
			continue
		}
		nr, err := strconv.Atoi(f[0])
		if err != nil {
			return nil, fmt.Errorf("unexpected group record %q", rec)
		}
		name := f[2]
		if u, err := url.PathUnescape(name); err == nil {
			name = u
		}
		vid, _ := strconv.Atoi(f[7])
		aggr, _ := strconv.Atoi(f[6])
		snoop, _ := strconv.Atoi(f[8])
		quer, tagNative := 0, 0
		if len(f) > 9 {
			quer, _ = strconv.Atoi(f[9])
		}
		if len(f) > 11 {
			tagNative, _ = strconv.Atoi(f[11])
		}
		out = append(out, GroupInfo{Nr: nr, Type: f[1], Name: name, Ports: parsePortList(f[3]), Tagged: parsePortList(f[4]),
			Color: f[5], VID: vid, Aggregated: aggr != 0, Snooping: snoop != 0, Querier: quer != 0, TagNative: tagNative != 0})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no groups in the reply %q", txt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Nr < out[j].Nr })
	return out, nil
}

// readSwitchGroups reads one switch. Only GET requests are sent.
func readSwitchGroups(d luminex.Device) SwitchGroups {
	sg := SwitchGroups{ID: d.ID, Name: d.Name, IP: d.IP, Serial: d.Serial, Model: d.Model}
	if d.Family != "gigacore" || d.APIKind != "gen1" {
		sg.Error = "Only GigaCore Gen1 switches can be read for now."
		return sg
	}
	auth, needsPw, err := findAuth(d, nil)
	switch {
	case err != nil:
		sg.Error = fmt.Sprintf("No answer from %s (%s).", d.IP, err)
		return sg
	case needsPw:
		sg.Error = "This switch has a password set, which this page does not handle yet."
		return sg
	}
	txt, err := getText(d, "/config/groups", auth)
	if err != nil {
		sg.Error = "Could not read the groups: " + err.Error()
		return sg
	}
	if sg.Groups, err = parseGen1Groups(txt); err != nil {
		sg.Error = err.Error()
		return sg
	}
	if p, err := getText(d, "/config/ports", auth); err == nil {
		for _, rec := range strings.Split(p, "|") {
			if strings.TrimSpace(rec) != "" {
				sg.PortCount++
			}
		}
	}
	return sg
}

// handleGroupsRead reads the groups of the given switches (all the GigaCores in the scan when none are given).
func handleGroupsRead(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	_ = readJSON(r, &body)
	var devs []luminex.Device
	if len(body.IDs) > 0 {
		var msg string
		if devs, msg = lookup(body.IDs); msg != "" {
			writeJSON(w, 400, map[string]string{"error": msg})
			return
		}
	} else {
		state.mu.Lock()
		for _, d := range state.devices {
			if d.Family == "gigacore" && d.Reachable && !(d.SharedIP && d.LinkRoute == "") {
				devs = append(devs, d)
			}
		}
		state.mu.Unlock()
	}
	out := make([]SwitchGroups, len(devs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, d := range devs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = readSwitchGroups(d)
		}()
	}
	wg.Wait()
	sort.SliceStable(out, func(i, j int) bool { return ipLess(out[i].IP, out[j].IP) })
	writeJSON(w, 200, map[string]any{"switches": out})
}

// ipLess orders addresses by number, so .2 comes before .10. Anything that is not an IPv4 address sorts as text.
func ipLess(a, b string) bool {
	x, y := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	if x == nil || y == nil {
		return a < b
	}
	return bytes.Compare(x, y) < 0
}

var groupNameOK = regexp.MustCompile(`^[A-Za-z0-9 _.\-]{1,16}$`)

// parseGroupNames reads the value of a "groupnames" setting: JSON like {"2":"FOH","3":"Video"}, group number to name.
func parseGroupNames(value string) (map[int]string, error) {
	raw := map[string]string{}
	if err := json.Unmarshal([]byte(value), &raw); err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("no group names to set")
	}
	out := map[int]string{}
	for k, v := range raw {
		n, err := strconv.Atoi(k)
		if err != nil || n < 2 || n > 99 {
			return nil, fmt.Errorf("%q is not a group number that can be named (groups 2 and up)", k)
		}
		if !groupNameOK.MatchString(v) {
			return nil, fmt.Errorf("%q cannot be a group name: use 1 to 16 letters, numbers, spaces, - _ or .", v)
		}
		out[n] = v
	}
	return out, nil
}

// groupNameSteps builds the rename of some groups on one Gen1 switch. The current groups are read first so every
// field of a changed group is sent back as it is (the switch's own page does the same), and the read-back checks
// that the names changed and nothing else about those groups did.
func groupNameSteps(d luminex.Device, value string, auth *Auth) ([]request, func() (string, error), error) {
	want, err := parseGroupNames(value)
	if err != nil {
		return nil, nil, err
	}
	txt, err := getText(d, "/config/groups", auth)
	if err != nil {
		return nil, nil, fmt.Errorf("could not read the current groups: %w", err)
	}
	before, err := parseGen1Groups(txt)
	if err != nil {
		return nil, nil, err
	}
	byNr := map[int]GroupInfo{}
	for _, g := range before {
		byNr[g.Nr] = g
	}
	form := url.Values{}
	var nrs []int
	for nr, name := range want {
		g, ok := byNr[nr]
		switch {
		case !ok:
			return nil, nil, fmt.Errorf("this switch has no group %d", nr)
		case g.Type != "Normal":
			return nil, nil, fmt.Errorf("group %d is the %s group and cannot be renamed", nr, g.Type)
		}
		nrs = append(nrs, nr)
		if g.Name == name {
			continue // already named
		}
		k := strconv.Itoa(nr)
		form.Set("grp_name_"+k, name)
		form.Set("grp_ports_"+k, joinInts(g.Ports))
		if len(g.Tagged) > 0 {
			form.Set("grp_tports_"+k, joinInts(g.Tagged))
		}
		form.Set("grp_vid_"+k, strconv.Itoa(g.VID))
		form.Set("grp_snooping_"+k, boolDigit(g.Snooping))
		form.Set("grp_querier_"+k, boolDigit(g.Querier))
	}
	sort.Ints(nrs)
	verify := func() (string, error) {
		t, err := getText(d, "/config/groups", auth)
		if err != nil {
			return "", err
		}
		after, err := parseGen1Groups(t)
		if err != nil {
			return "", err
		}
		now := map[int]GroupInfo{}
		for _, g := range after {
			now[g.Nr] = g
		}
		var got []string
		for _, nr := range nrs {
			a, b := now[nr], byNr[nr]
			if a.Name != want[nr] {
				return "", fmt.Errorf("group %d still reports the name %q", nr, a.Name)
			}
			if joinInts(a.Ports) != joinInts(b.Ports) || joinInts(a.Tagged) != joinInts(b.Tagged) || a.VID != b.VID {
				return "", fmt.Errorf("group %d's ports or VLAN ID changed, which was not asked for", nr)
			}
			got = append(got, fmt.Sprintf("%d=%s", nr, a.Name))
		}
		return strings.Join(got, ", "), nil
	}
	if len(form) == 0 {
		return nil, verify, nil // every name is already as asked
	}
	return []request{formPost(d, "/config/groups", form.Encode()), formPost(d, "/config/icfg_conf_save", "save=1")}, verify, nil
}

func joinInts(a []int) string {
	s := make([]string, len(a))
	for i, n := range a {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ",")
}

func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// A group profile is a layout of the Normal groups (2 and up) on a switch: each group's name, VLAN ID, untagged ports
// and tagged ports. It is saved from one switch and applied to others with the same number of ports.
type ProfileGroup struct {
	Nr     int    `json:"nr"`
	Name   string `json:"name"`
	VID    int    `json:"vid"`
	Ports  []int  `json:"ports"`
	Tagged []int  `json:"tagged"`
}

// ProfileISL sets which ports are inter-switch links (group 0). A profile without it leaves them alone.
type ProfileISL struct {
	Ports []int `json:"ports"`
}

type GroupProfile struct {
	Name      string         `json:"name"`
	PortCount int            `json:"portCount"`
	Groups    []ProfileGroup `json:"groups"`
	ISL       *ProfileISL    `json:"isl,omitempty"`
}

// parseGroupProfile checks a profile on its own, before any switch is looked at.
func parseGroupProfile(value string) (*GroupProfile, error) {
	var p GroupProfile
	if err := json.Unmarshal([]byte(value), &p); err != nil || len(p.Groups) == 0 {
		return nil, fmt.Errorf("the profile has no groups")
	}
	if p.PortCount < 1 || p.PortCount > 52 {
		return nil, fmt.Errorf("the profile's port count %d is not usable", p.PortCount)
	}
	seenNr, seenPort, vids := map[int]bool{}, map[int]int{}, map[int]int{}
	for i := range p.Groups {
		g := &p.Groups[i]
		if g.Nr < 2 || g.Nr > 99 || seenNr[g.Nr] {
			return nil, fmt.Errorf("group number %d cannot be used (groups 2 and up, each once)", g.Nr)
		}
		seenNr[g.Nr] = true
		if !groupNameOK.MatchString(g.Name) {
			return nil, fmt.Errorf("group %d: %q cannot be a group name (1 to 16 letters, numbers, spaces, - _ or .)", g.Nr, g.Name)
		}
		if g.VID < 2 || g.VID > 4094 {
			return nil, fmt.Errorf("group %d: VLAN ID %d is not usable (2 to 4094)", g.Nr, g.VID)
		}
		if other, dup := vids[g.VID]; dup {
			return nil, fmt.Errorf("groups %d and %d both use VLAN ID %d", other, g.Nr, g.VID)
		}
		vids[g.VID] = g.Nr
		if g.Ports == nil {
			g.Ports = []int{}
		}
		if g.Tagged == nil {
			g.Tagged = []int{}
		}
		sort.Ints(g.Ports)
		sort.Ints(g.Tagged)
		for _, port := range g.Ports {
			if port < 1 || port > p.PortCount {
				return nil, fmt.Errorf("group %d: port %d does not exist (1 to %d)", g.Nr, port, p.PortCount)
			}
			if other, dup := seenPort[port]; dup {
				return nil, fmt.Errorf("port %d is in both group %d and group %d", port, other, g.Nr)
			}
			seenPort[port] = g.Nr
		}
		for _, port := range g.Tagged {
			if port < 1 || port > p.PortCount {
				return nil, fmt.Errorf("group %d: tagged port %d does not exist (1 to %d)", g.Nr, port, p.PortCount)
			}
			for _, u := range g.Ports {
				if u == port {
					return nil, fmt.Errorf("group %d: port %d is both untagged and tagged", g.Nr, port)
				}
			}
		}
	}
	sort.Slice(p.Groups, func(i, j int) bool { return p.Groups[i].Nr < p.Groups[j].Nr })
	if p.ISL != nil {
		if p.ISL.Ports == nil {
			p.ISL.Ports = []int{}
		}
		sort.Ints(p.ISL.Ports)
		seen := map[int]bool{}
		for _, port := range p.ISL.Ports {
			if port < 1 || port > p.PortCount {
				return nil, fmt.Errorf("ISL port %d does not exist (1 to %d)", port, p.PortCount)
			}
			if seen[port] {
				return nil, fmt.Errorf("ISL port %d is listed twice", port)
			}
			seen[port] = true
			if other, dup := seenPort[port]; dup {
				return nil, fmt.Errorf("port %d is both an ISL link and in group %d", port, other)
			}
			for _, g := range p.Groups {
				for _, t := range g.Tagged {
					if t == port {
						return nil, fmt.Errorf("port %d is both an ISL link and tagged in group %d", port, g.Nr)
					}
				}
			}
		}
	}
	return &p, nil
}

// groupProfileSteps builds the writes that make one Gen1 switch match the profile. Only groups that differ are sent,
// each with all of its fields (as the switch's own page does). The read-back checks every group in the profile and
// that no port it puts in a group is still in another one, and that the ports it takes out of a group are back in
// Management. A VLAN ID that a group outside the profile already uses is refused before anything is sent. Ports that leave Management stop answering on the switch's management
// address (it is on Management's VLAN), which is the caller's to warn about; reading back shows whether this
// computer can still reach the switch.
func groupProfileSteps(d luminex.Device, value string, auth *Auth) ([]request, func() (string, error), error) {
	p, err := parseGroupProfile(value)
	if err != nil {
		return nil, nil, err
	}
	pt, err := getText(d, "/config/ports", auth)
	if err != nil {
		return nil, nil, fmt.Errorf("could not read the ports: %w", err)
	}
	ports := 0
	for _, rec := range strings.Split(pt, "|") {
		if strings.TrimSpace(rec) != "" {
			ports++
		}
	}
	if ports != p.PortCount {
		return nil, nil, fmt.Errorf("this switch has %d ports and the profile is for %d", ports, p.PortCount)
	}
	txt, err := getText(d, "/config/groups", auth)
	if err != nil {
		return nil, nil, fmt.Errorf("could not read the current groups: %w", err)
	}
	before, err := parseGen1Groups(txt)
	if err != nil {
		return nil, nil, err
	}
	cur := map[int]GroupInfo{}
	for _, g := range before {
		cur[g.Nr] = g
	}
	isl := map[int]bool{}
	if p.ISL == nil { // when the profile does not set the ISL ports, the switch's own stay where they are
		for _, port := range cur[0].Ports {
			isl[port] = true
		}
	}
	inProfile := map[int]bool{}
	for _, pg := range p.Groups {
		inProfile[pg.Nr] = true
	}
	for _, pg := range p.Groups { // a VLAN ID can belong to one group only, and the profile cannot see the groups it leaves out
		for _, g := range before {
			if g.Type == "Normal" && !inProfile[g.Nr] && g.VID == pg.VID {
				return nil, nil, fmt.Errorf("VLAN ID %d is already used by group %d (%s) on this switch, which the profile does not set", pg.VID, g.Nr, g.Name)
			}
		}
	}
	form := url.Values{}
	if p.ISL != nil {
		c := cur[0]
		if joinInts(sortedInts(c.Ports)) != joinInts(p.ISL.Ports) {
			form.Set("grp_ports_0", joinInts(p.ISL.Ports))
			form.Set("grp_aggr_0", boolDigit(c.Aggregated))
			form.Set("grp_tag_native_vlan_0", boolDigit(c.TagNative))
		}
	}
	for _, pg := range p.Groups {
		c, ok := cur[pg.Nr]
		switch {
		case !ok || c.Type != "Normal":
			return nil, nil, fmt.Errorf("this switch has no group %d to set", pg.Nr)
		}
		for _, port := range append(append([]int{}, pg.Ports...), pg.Tagged...) {
			if isl[port] {
				return nil, nil, fmt.Errorf("port %d is a link to another switch (ISL) here and cannot be put in a group", port)
			}
		}
		if c.Name == pg.Name && c.VID == pg.VID && joinInts(sortedInts(c.Ports)) == joinInts(pg.Ports) && joinInts(sortedInts(c.Tagged)) == joinInts(pg.Tagged) {
			continue
		}
		k := strconv.Itoa(pg.Nr)
		form.Set("grp_name_"+k, pg.Name)
		form.Set("grp_ports_"+k, joinInts(pg.Ports))
		if len(pg.Tagged) > 0 {
			form.Set("grp_tports_"+k, joinInts(pg.Tagged))
		}
		form.Set("grp_vid_"+k, strconv.Itoa(pg.VID))
		form.Set("grp_snooping_"+k, boolDigit(c.Snooping))
		form.Set("grp_querier_"+k, boolDigit(c.Querier))
	}
	verify := func() (string, error) {
		t, err := getText(d, "/config/groups", auth)
		if err != nil {
			return "", err
		}
		after, err := parseGen1Groups(t)
		if err != nil {
			return "", err
		}
		now := map[int]GroupInfo{}
		for _, g := range after {
			now[g.Nr] = g
		}
		claimed := map[int]int{} // port → the profile group it is untagged in
		var got []string
		if p.ISL != nil && joinInts(sortedInts(now[0].Ports)) != joinInts(p.ISL.Ports) {
			return "", fmt.Errorf("the ISL ports did not end up as the profile says (they are %s)", joinInts(now[0].Ports))
		}
		for _, pg := range p.Groups {
			a := now[pg.Nr]
			if a.Name != pg.Name || a.VID != pg.VID || joinInts(sortedInts(a.Ports)) != joinInts(pg.Ports) || joinInts(sortedInts(a.Tagged)) != joinInts(pg.Tagged) {
				return "", fmt.Errorf("group %d did not end up as the profile says (it has %q, VLAN %d, ports %s, tagged %s)", pg.Nr, a.Name, a.VID, joinInts(a.Ports), joinInts(a.Tagged))
			}
			for _, port := range pg.Ports {
				claimed[port] = pg.Nr
			}
			got = append(got, fmt.Sprintf("%d=%s", pg.Nr, pg.Name))
		}
		// A port the profile put in a group must have left every other group, the ones outside the profile included.
		for _, g := range after {
			for _, port := range g.Ports {
				if nr, ok := claimed[port]; ok && nr != g.Nr {
					return "", fmt.Errorf("port %d is in both group %d and %s (group %d)", port, nr, g.Name, g.Nr)
				}
			}
		}
		// A port the profile took out of one of its groups, and gave to none, must be back in Management (or be an ISL link).
		home := map[int]bool{}
		for _, port := range append(append([]int{}, now[1].Ports...), now[0].Ports...) {
			home[port] = true
		}
		for _, pg := range p.Groups {
			for _, port := range cur[pg.Nr].Ports {
				if _, ok := claimed[port]; !ok && !home[port] {
					return "", fmt.Errorf("port %d left group %d but did not go back to Management", port, pg.Nr)
				}
			}
		}
		return strings.Join(got, ", "), nil
	}
	if len(form) == 0 {
		return nil, verify, nil // already as the profile says
	}
	return []request{formPost(d, "/config/groups", form.Encode()), formPost(d, "/config/icfg_conf_save", "save=1")}, verify, nil
}

func sortedInts(a []int) []int {
	b := append([]int{}, a...)
	sort.Ints(b)
	return b
}
