package commission

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

// What a real GigaCore 10 (Gen1 v3.1.5) answered on 6 Nov 2026, cut to a few groups.
const realGroups = "0/ISL/ISL///#999999/0/1/0/0/0/0|1/Management/Management/1,2,3,4,5,6,7,8,9,10//#325197/0/1/1/1/0/0|2/Normal/Group02///#E80000/0/200/1/1/0/0|3/Normal/Show%20Net/4,5/6/#32CD32/0/300/1/1/0/0"

func TestParseGen1Groups(t *testing.T) {
	g, err := parseGen1Groups(realGroups)
	if err != nil || len(g) != 4 {
		t.Fatalf("parse: %v %+v", err, g)
	}
	if g[1].Type != "Management" || g[1].Name != "Management" || len(g[1].Ports) != 10 || g[1].VID != 1 || g[1].Color != "#325197" {
		t.Errorf("management group wrong: %+v", g[1])
	}
	if g[3].Name != "Show Net" || g[3].VID != 300 || len(g[3].Ports) != 2 || g[3].Ports[1] != 5 || len(g[3].Tagged) != 1 || g[3].Tagged[0] != 6 {
		t.Errorf("a group with ports and a url-escaped name: %+v", g[3])
	}
	if g[2].Ports == nil || g[2].Tagged == nil {
		t.Error("a group with no ports should have empty lists, not null")
	}
	if _, err := parseGen1Groups(""); err == nil {
		t.Error("an empty reply should be an error")
	}
	if _, err := parseGen1Groups("x/Normal/a/1//#fff/0/200/1/1/0/0"); err == nil {
		t.Error("a record without a numeric group number should be an error")
	}
}

func TestReadSwitchGroupsOnlyReads(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		switch r.URL.Path {
		case "/config/groups":
			fmt.Fprint(w, realGroups)
		case "/config/ports":
			fmt.Fprint(w, "1/x|2/x|3/x|")
		default:
			fmt.Fprint(w, "1/0/0") // the probe for findAuth
		}
	}))
	defer srv.Close()
	oldDry := luminex.DryRun
	luminex.DryRun = false
	defer func() { luminex.DryRun = oldDry }()
	redirect(t, srv.URL)

	sg := readSwitchGroups(luminex.Device{ID: "a", IP: "127.0.0.1", Family: "gigacore", APIKind: "gen1", Serial: "LUM1"})
	if sg.Error != "" || len(sg.Groups) != 4 || sg.PortCount != 3 {
		t.Fatalf("read: %+v", sg)
	}
	for _, m := range methods {
		if m != "GET" {
			t.Errorf("reading groups must only GET, saw %s", m)
		}
	}
	if g := readSwitchGroups(luminex.Device{ID: "b", IP: "127.0.0.1", Family: "luminode", APIKind: "luminode"}); g.Error == "" {
		t.Error("a LumiNode has no groups and should say so")
	}
}

// fakeGroupsSwitch keeps the groups as parsed records and applies a POST /config/groups the way the switch does.
type fakeGroupsSwitch struct {
	groups    []GroupInfo
	posts     []string
	saves     int
	portCount int
}

func (f *fakeGroupsSwitch) text() string {
	var recs []string
	for _, g := range f.groups {
		recs = append(recs, fmt.Sprintf("%d/%s/%s/%s/%s/%s/0/%d/%s/%s/0/0", g.Nr, g.Type, url.PathEscape(g.Name), joinInts(g.Ports), joinInts(g.Tagged), g.Color, g.VID, boolDigit(g.Snooping), boolDigit(g.Querier)))
	}
	return strings.Join(recs, "|")
}

func (f *fakeGroupsSwitch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	switch {
	case r.Method == "GET" && r.URL.Path == "/config/groups":
		fmt.Fprint(w, f.text())
	case r.Method == "GET" && r.URL.Path == "/config/ports":
		n := f.portCount
		if n == 0 {
			n = 10
		}
		fmt.Fprint(w, strings.Repeat("1/x|", n))
	case r.Method == "POST" && r.URL.Path == "/config/groups":
		f.posts = append(f.posts, r.PostForm.Encode())
		claimed := map[int]bool{}
		for i := range f.groups {
			k := fmt.Sprintf("_%d", f.groups[i].Nr)
			if n := r.PostForm.Get("grp_name" + k); n != "" {
				f.groups[i].Name = n
			}
			if _, ok := r.PostForm["grp_ports"+k]; ok {
				f.groups[i].Ports = parsePortList(r.PostForm.Get("grp_ports" + k))
				f.groups[i].Tagged = parsePortList(r.PostForm.Get("grp_tports" + k))
				if v, err := strconv.Atoi(r.PostForm.Get("grp_vid" + k)); err == nil {
					f.groups[i].VID = v
				}
				for _, p := range f.groups[i].Ports {
					claimed[p] = true
				}
			}
		}
		// The switch takes a port out of any other group it was in, and a port in no group falls back to Management.
		for i := range f.groups {
			if _, ok := r.PostForm[fmt.Sprintf("grp_ports_%d", f.groups[i].Nr)]; ok || f.groups[i].Type == "ISL" {
				continue
			}
			var keep []int
			for _, p := range f.groups[i].Ports {
				if !claimed[p] {
					keep = append(keep, p)
				}
			}
			f.groups[i].Ports = append([]int{}, keep...)
		}
		inGroup := map[int]bool{}
		for _, g := range f.groups {
			if g.Type != "Management" {
				for _, p := range g.Ports {
					inGroup[p] = true
				}
			}
		}
		n := f.portCount
		if n == 0 {
			n = 10
		}
		for i := range f.groups {
			if f.groups[i].Type == "Management" {
				f.groups[i].Ports = []int{}
				for p := 1; p <= n; p++ {
					if !inGroup[p] {
						f.groups[i].Ports = append(f.groups[i].Ports, p)
					}
				}
			}
		}
	case r.Method == "POST" && r.URL.Path == "/config/icfg_conf_save":
		f.saves++
	default:
		fmt.Fprint(w, "1/0/0")
	}
}

func withGroupsSwitch(t *testing.T) (*fakeGroupsSwitch, luminex.Device) {
	t.Helper()
	g, _ := parseGen1Groups(realGroups)
	f := &fakeGroupsSwitch{groups: g}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	oldDry := luminex.DryRun
	luminex.DryRun = false
	t.Cleanup(func() { luminex.DryRun = oldDry })
	redirect(t, srv.URL)
	return f, luminex.Device{ID: "a", IP: "127.0.0.1", Family: "gigacore", APIKind: "gen1", Serial: "LUM1"}
}

func TestRenameGroups(t *testing.T) {
	f, d := withGroupsSwitch(t)
	s, msg := applySetting(d, "groupnames", `{"2":"FOH","3":"Video"}`, &Auth{Username: "admin"})
	if s != "done" || f.groups[2].Name != "FOH" || f.groups[3].Name != "Video" || f.saves != 1 || len(f.posts) != 1 {
		t.Fatalf("rename: %s %q groups=%+v posts=%v saves=%d", s, msg, f.groups, f.posts, f.saves)
	}
	// The group that had ports keeps them, its tagged port, and its VLAN ID; they were sent back as they were.
	q, _ := url.ParseQuery(f.posts[0])
	if q.Get("grp_ports_3") != "4,5" || q.Get("grp_tports_3") != "6" || q.Get("grp_vid_3") != "300" || q.Get("grp_snooping_3") != "1" {
		t.Errorf("the other fields of a renamed group must be sent back unchanged: %v", q)
	}
	if q.Get("grp_name_1") != "" || q.Get("grp_ports_1") != "" {
		t.Errorf("a group that was not renamed must not be sent: %v", q)
	}
	// Asking for the names it already has sends nothing.
	n := len(f.posts)
	if s, _ := applySetting(d, "groupnames", `{"2":"FOH"}`, &Auth{Username: "admin"}); s != "done" || len(f.posts) != n {
		t.Errorf("a name already in place should not be written again: %s posts=%d", s, len(f.posts))
	}
}

func TestRenameGroupsRefusals(t *testing.T) {
	f, d := withGroupsSwitch(t)
	for _, v := range []string{`{"1":"Mine"}`, `{"0":"Mine"}`, `{"2":"a/b"}`, `{"2":""}`, `{"2":"this name is far too long"}`, `{"x":"A"}`, `{}`, `{"30":"A"}`} {
		if s, _ := applySetting(d, "groupnames", v, &Auth{Username: "admin"}); s != "failed" {
			t.Errorf("%s should be refused, got %s", v, s)
		}
	}
	if len(f.posts) != 0 || f.saves != 0 {
		t.Errorf("nothing refused may be sent: posts=%v saves=%d", f.posts, f.saves)
	}
	if checkValue("groupnames", "gen2", `{"2":"A"}`) == "" {
		t.Error("Gen2 should be refused for now")
	}
}

func TestRenameGroupsDryRunSendsNothing(t *testing.T) {
	f, d := withGroupsSwitch(t)
	luminex.DryRun = true
	s, msg := applySetting(d, "groupnames", `{"2":"FOH"}`, &Auth{Username: "admin"})
	if s != "dry_run" || !strings.Contains(msg, "/config/groups") || len(f.posts) != 0 || f.saves != 0 || f.groups[2].Name != "Group02" {
		t.Errorf("dry run: %s %q posts=%v", s, msg, f.posts)
	}
}

func profileJSON(t *testing.T, p GroupProfile) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApplyGroupProfileMovesPorts(t *testing.T) {
	f, d := withGroupsSwitch(t)
	// The switch starts with ports 1-10 in Management and group 3 holding 4,5 (tagged 6) from realGroups; make it a clean layout first.
	prof := GroupProfile{Name: "FOH rack", PortCount: 10, Groups: []ProfileGroup{
		{Nr: 2, Name: "Lights", VID: 200, Ports: []int{1, 2}, Tagged: []int{}},
		{Nr: 3, Name: "Show Net", VID: 300, Ports: []int{4, 5}, Tagged: []int{6}},
	}}
	s, msg := applySetting(d, "groupprofile", profileJSON(t, prof), &Auth{Username: "admin"})
	if s != "done" || f.saves != 1 || len(f.posts) != 1 {
		t.Fatalf("apply: %s %q posts=%v saves=%d", s, msg, f.posts, f.saves)
	}
	if joinInts(f.groups[2].Ports) != "1,2" || f.groups[2].Name != "Lights" {
		t.Errorf("group 2: %+v", f.groups[2])
	}
	if joinInts(f.groups[1].Ports) != "3,6,7,8,9,10" {
		t.Errorf("the ports taken from Management should be gone from it: %v", f.groups[1].Ports)
	}
	q, _ := url.ParseQuery(f.posts[0])
	if q.Get("grp_name_3") != "" {
		t.Errorf("group 3 already matched and must not be sent: %v", q)
	}
	// Applying the same profile again changes nothing.
	n := len(f.posts)
	if s, _ := applySetting(d, "groupprofile", profileJSON(t, prof), &Auth{Username: "admin"}); s != "done" || len(f.posts) != n {
		t.Errorf("a switch that already matches should not be written: %s posts=%d", s, len(f.posts))
	}
	// A port taken out of a group goes back to Management.
	prof.Groups[0].Ports = []int{1}
	if s, msg := applySetting(d, "groupprofile", profileJSON(t, prof), &Auth{Username: "admin"}); s != "done" || joinInts(f.groups[1].Ports) != "2,3,6,7,8,9,10" {
		t.Errorf("shrinking a group: %s %q mgmt=%v", s, msg, f.groups[1].Ports)
	}
}

func TestApplyGroupProfileRefusals(t *testing.T) {
	f, d := withGroupsSwitch(t)
	f.groups[0].Ports = []int{9, 10} // ISL ports on this switch
	adm := &Auth{Username: "admin"}
	good := ProfileGroup{Nr: 2, Name: "A", VID: 200, Ports: []int{1}, Tagged: []int{}}
	cases := map[string]GroupProfile{
		"port count differs":  {PortCount: 12, Groups: []ProfileGroup{good}},
		"isl port":            {PortCount: 10, Groups: []ProfileGroup{{Nr: 2, Name: "A", VID: 200, Ports: []int{9}}}},
		"group missing":       {PortCount: 10, Groups: []ProfileGroup{{Nr: 40, Name: "A", VID: 4000, Ports: []int{1}}}},
		"management renamed":  {PortCount: 10, Groups: []ProfileGroup{{Nr: 1, Name: "A", VID: 5, Ports: []int{1}}}},
		"port in two groups":  {PortCount: 10, Groups: []ProfileGroup{good, {Nr: 3, Name: "B", VID: 300, Ports: []int{1}}}},
		"same vid twice":      {PortCount: 10, Groups: []ProfileGroup{good, {Nr: 3, Name: "B", VID: 200, Ports: []int{2}}}},
		"vid 1":               {PortCount: 10, Groups: []ProfileGroup{{Nr: 2, Name: "A", VID: 1, Ports: []int{1}}}},
		"port out of range":   {PortCount: 10, Groups: []ProfileGroup{{Nr: 2, Name: "A", VID: 200, Ports: []int{11}}}},
		"untagged and tagged": {PortCount: 10, Groups: []ProfileGroup{{Nr: 2, Name: "A", VID: 200, Ports: []int{1}, Tagged: []int{1}}}},
		"bad name":            {PortCount: 10, Groups: []ProfileGroup{{Nr: 2, Name: "a/b", VID: 200, Ports: []int{1}}}},
	}
	for what, p := range cases {
		if s, _ := applySetting(d, "groupprofile", profileJSON(t, p), adm); s != "failed" {
			t.Errorf("%s should be refused, got %s", what, s)
		}
	}
	if len(f.posts) != 0 || f.saves != 0 {
		t.Errorf("nothing refused may be sent: %v", f.posts)
	}
	if checkValue("groupprofile", "gen2", profileJSON(t, GroupProfile{PortCount: 10, Groups: []ProfileGroup{good}})) == "" {
		t.Error("Gen2 should be refused for now")
	}
}

func TestApplyGroupProfileDryRunSendsNothing(t *testing.T) {
	f, d := withGroupsSwitch(t)
	luminex.DryRun = true
	p := GroupProfile{PortCount: 10, Groups: []ProfileGroup{{Nr: 2, Name: "Lights", VID: 200, Ports: []int{1, 2}}}}
	s, msg := applySetting(d, "groupprofile", profileJSON(t, p), &Auth{Username: "admin"})
	if s != "dry_run" || !strings.Contains(msg, "grp_ports_2=1%2C2") || len(f.posts) != 0 || f.saves != 0 {
		t.Errorf("dry run: %s %q posts=%v", s, msg, f.posts)
	}
}

func TestApplyGroupProfileSetsISLPorts(t *testing.T) {
	f, d := withGroupsSwitch(t)
	adm := &Auth{Username: "admin"}
	p := GroupProfile{PortCount: 10, ISL: &ProfileISL{Ports: []int{9, 10}}, Groups: []ProfileGroup{{Nr: 2, Name: "Lights", VID: 200, Ports: []int{1}}}}
	if s, msg := applySetting(d, "groupprofile", profileJSON(t, p), adm); s != "done" || joinInts(f.groups[0].Ports) != "9,10" {
		t.Fatalf("isl: %s %q isl=%v", s, msg, f.groups[0].Ports)
	}
	if joinInts(f.groups[1].Ports) != "2,3,6,7,8" {
		t.Errorf("ports taken by ISL and a group should be out of Management: %v", f.groups[1].Ports)
	}
	q, _ := url.ParseQuery(f.posts[0])
	if q.Get("grp_ports_0") != "9,10" || q.Get("grp_aggr_0") != "0" || q.Get("grp_tag_native_vlan_0") != "0" {
		t.Errorf("an ISL change is sent with the page's other ISL fields: %v", q)
	}
	// Asking for the same again writes nothing; a profile without ISL leaves them alone.
	n := len(f.posts)
	if s, _ := applySetting(d, "groupprofile", profileJSON(t, p), adm); s != "done" || len(f.posts) != n {
		t.Errorf("already matching: %s posts=%d", s, len(f.posts))
	}
	p.ISL = nil
	p.Groups[0].Name = "Lights 2"
	if s, _ := applySetting(d, "groupprofile", profileJSON(t, p), adm); s != "done" || joinInts(f.groups[0].Ports) != "9,10" {
		t.Errorf("a profile without ISL must leave ISL ports alone: %v", f.groups[0].Ports)
	}
	// ...and may not put one of those ISL ports in a group.
	p.Groups[0].Ports = []int{9}
	if s, _ := applySetting(d, "groupprofile", profileJSON(t, p), adm); s != "failed" {
		t.Error("a group port that is an ISL link on the switch must be refused when the profile does not move the ISL")
	}
	// Clearing the ISL ports sends them back to Management.
	p.ISL, p.Groups[0].Ports = &ProfileISL{Ports: []int{}}, []int{1}
	if s, _ := applySetting(d, "groupprofile", profileJSON(t, p), adm); s != "done" || len(f.groups[0].Ports) != 0 {
		t.Errorf("clearing ISL: %v", f.groups[0].Ports)
	}
	for what, bad := range map[string]GroupProfile{
		"isl port in a group":   {PortCount: 10, ISL: &ProfileISL{Ports: []int{1}}, Groups: []ProfileGroup{{Nr: 2, Name: "A", VID: 200, Ports: []int{1}}}},
		"isl port tagged":       {PortCount: 10, ISL: &ProfileISL{Ports: []int{1}}, Groups: []ProfileGroup{{Nr: 2, Name: "A", VID: 200, Tagged: []int{1}}}},
		"isl port out of range": {PortCount: 10, ISL: &ProfileISL{Ports: []int{11}}, Groups: []ProfileGroup{{Nr: 2, Name: "A", VID: 200}}},
		"isl port listed twice": {PortCount: 10, ISL: &ProfileISL{Ports: []int{3, 3}}, Groups: []ProfileGroup{{Nr: 2, Name: "A", VID: 200}}},
	} {
		if err := saveProfileCheck(bad); err == nil {
			t.Errorf("%s should be refused", what)
		}
	}
}

func saveProfileCheck(p GroupProfile) error {
	b, _ := json.Marshal(p)
	_, err := parseGroupProfile(string(b))
	return err
}

// Groups the profile leaves out are still looked at: their VLAN IDs cannot be reused, and a port taken from one of
// them has to have left it.
func TestApplyGroupProfileLooksAtGroupsOutsideIt(t *testing.T) {
	f, d := withGroupsSwitch(t)
	adm := &Auth{Username: "admin"}
	// Group 3 (VLAN 300, ports 4,5) is not in this profile.
	clash := GroupProfile{PortCount: 10, Groups: []ProfileGroup{{Nr: 2, Name: "Lights", VID: 300, Ports: []int{1}}}}
	if s, msg := applySetting(d, "groupprofile", profileJSON(t, clash), adm); s != "failed" || !strings.Contains(msg, "VLAN ID 300") || len(f.posts) != 0 {
		t.Errorf("a VLAN ID used by a group outside the profile should be refused before sending: %s %q posts=%v", s, msg, f.posts)
	}
	take := GroupProfile{PortCount: 10, Groups: []ProfileGroup{{Nr: 2, Name: "Lights", VID: 200, Ports: []int{4}}}}
	if s, msg := applySetting(d, "groupprofile", profileJSON(t, take), adm); s != "done" || joinInts(f.groups[3].Ports) != "5" {
		t.Errorf("taking port 4 from group 3: %s %q group3=%v", s, msg, f.groups[3].Ports)
	}
	// A switch that left the port in both groups must not be reported as done.
	f.groups[3].Ports = []int{4, 5}
	_, verify, err := groupProfileSteps(d, profileJSON(t, take), adm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verify(); err == nil || !strings.Contains(err.Error(), "port 4 is in both") {
		t.Errorf("a port left in two groups should fail the read-back, got %v", err)
	}
}

func TestIPLess(t *testing.T) {
	if !ipLess("192.168.2.2", "192.168.2.10") || ipLess("192.168.2.10", "192.168.2.2") || !ipLess("10.0.0.1", "192.168.0.1") {
		t.Error("addresses should sort by number")
	}
}
