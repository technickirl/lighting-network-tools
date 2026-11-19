package nodeconfig

// RDM on a LumiNode's DMX ports. The node's REST API turns RDM on per port (the
// DMX IO's `rdm`) and lists the UIDs it has found; everything else goes over
// Art-Net RDM (internal/artnet) to the port-address the node gives the port as
// `rdm_universe`. Found on a real LumiNode 12 (v2.9.1) on 12 Nov 2026: turning
// rdm on for a port fed by sACN universe N set its rdm_universe to N by itself,
// and ArtTodRequest / ArtRdm to Art-Net port-address N then reached the fixture,
// with the port still on sACN. Turning rdm off leaves rdm_universe as it was.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/technickirl/lighting-network-tools/internal/artnet"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

// rdmController is replaced by tests.
var rdmController = artnet.Shared

const todWait = 2500 * time.Millisecond

// rdmPort is one DMX port as the RDM tab shows it.
type rdmPort struct {
	Port     int    `json:"port"` // 1-based, as printed on the node
	IOID     int    `json:"ioId"`
	Name     string `json:"name"`
	IOType   string `json:"ioType"`
	RDM      bool   `json:"rdm"`
	Universe int    `json:"universe"` // the Art-Net port-address RDM uses, -1 when the node has none
	Source   string `json:"source"`   // what feeds the port, e.g. "sACN U1"
	Count    int    `json:"count"`    // UIDs the node lists for the port (-1 unknown)
}

// rdmFixture is one RDM device on a port.
type rdmFixture struct {
	UID             string   `json:"uid"`
	ManufacturerID  int      `json:"manufacturerId"`
	Manufacturer    string   `json:"manufacturer"`
	ModelID         int      `json:"modelId"`
	Model           string   `json:"model"`
	Category        string   `json:"category"`
	Label           string   `json:"label"`
	Software        string   `json:"software"`
	Start           *int     `json:"start"` // nil: no DMX footprint, or not known
	Footprint       int      `json:"footprint"`
	Personality     int      `json:"personality"`
	Personalities   int      `json:"personalities"`
	PersonalityName string   `json:"personalityName"`
	Sensors         int      `json:"sensors"`
	Identify        bool     `json:"identify"`
	Partial         bool     `json:"partial,omitempty"` // only UID and Identify are filled: merge, don't replace
	SubDevices      int      `json:"subDevices"`
	Error           string   `json:"error,omitempty"`
	Overlaps        []string `json:"overlaps,omitempty"` // UIDs whose channels it shares
	PastEnd         bool     `json:"pastEnd,omitempty"`  // runs past channel 512
}

func dmxIOs(ip string) ([]obj, error) {
	ios, err := lumiObjects(ip, "/api/IO")
	if err != nil {
		return nil, err
	}
	var out []obj
	for _, io := range ios {
		if str(io, "io_class", "") == "dmx" {
			out = append(out, io)
		}
	}
	sort.Slice(out, func(i, j int) bool { return numOr(out[i], "port_number", 0) < numOr(out[j], "port_number", 0) })
	return out, nil
}

func dmxIOFor(ip string, port int) (obj, error) {
	ios, err := dmxIOs(ip)
	if err != nil {
		return nil, err
	}
	for _, io := range ios {
		if numOr(io, "port_number", -1) == port-1 {
			return io, nil
		}
	}
	return nil, fmt.Errorf("the node has no DMX port %d", port)
}

func connectedUIDs(ip string, port int) ([]string, error) {
	var uids []string
	err := lumiGetInto(ip, "/api/rdm/"+strconv.Itoa(port-1)+"/connected_devices", &uids)
	return uids, err
}

// rdmPorts reads every DMX port's RDM state, what feeds it and how many devices
// the node has found on it.
func rdmPorts(ip string) ([]rdmPort, error) {
	ios, err := dmxIOs(ip)
	if err != nil {
		return nil, err
	}
	ports := make([]rdmPort, len(ios))
	var wg sync.WaitGroup
	for i, io := range ios {
		ports[i] = rdmPort{Port: numOr(io, "port_number", 0) + 1, IOID: numOr(io, "id", -1), Name: str(io, "name", ""),
			IOType: str(io, "io_type", ""), RDM: io["rdm"] == true, Universe: numOr(io, "rdm_universe", -1), Count: -1}
		wg.Add(1)
		go func(p *rdmPort) {
			defer wg.Done()
			var cands []obj
			if lumiGetInto(ip, fmt.Sprintf("/api/IO/%d/artrdm_candidates", p.IOID), &cands) == nil && len(cands) > 0 {
				p.Source = ioLabel(cands[0])
			}
			if p.RDM {
				if uids, err := connectedUIDs(ip, p.Port); err == nil {
					p.Count = len(uids)
				}
			}
		}(&ports[i])
	}
	wg.Wait()
	return ports, nil
}

func ioLabel(io obj) string {
	u := numOr(io, "universe", -1)
	switch str(io, "io_class", "") {
	case "sacn":
		return fmt.Sprintf("sACN U%d", u)
	case "artnet":
		return fmt.Sprintf("Art-Net %d", u)
	}
	return str(io, "name", "")
}

// setPortRDM turns RDM on or off for one port: the whole IO goes back with only
// rdm changed (a PUT resets what it leaves out), then it is read back.
func setPortRDM(ip string, port int, on bool) error {
	io, err := dmxIOFor(ip, port)
	if err != nil {
		return err
	}
	if on && str(io, "io_type", "") != "output" {
		return fmt.Errorf("DMX port %d is not an output, so it cannot do RDM", port)
	}
	if io["rdm"] == on {
		return nil
	}
	upd := obj{}
	for k, v := range io {
		upd[k] = v
	}
	upd["rdm"] = on
	id := numOr(io, "id", -1)
	if _, err := lumiDo("PUT", ip, "/api/IO/"+strconv.Itoa(id), upd); err != nil {
		return err
	}
	if luminex.DryRun {
		return nil
	}
	back, err := lumiObject(ip, "/api/IO/"+strconv.Itoa(id))
	if err != nil {
		return err
	}
	if back["rdm"] != on {
		return fmt.Errorf("DMX port %d still reads rdm %v after the change", port, back["rdm"])
	}
	return nil
}

// readFixtures lists the RDM devices on one port with their settings: over
// Art-Net when it can, else from what the node has overheard.
func readFixtures(ip string, port int) (*rdmRead, error) {
	pa, err := rdmTarget(ip, port)
	if err != nil {
		return nil, err
	}
	out := &rdmRead{Port: port, Universe: int(pa)}
	ctl, err := rdmController()
	var fixtures []rdmFixture
	var others []int
	if err == nil {
		others, err = sharingPorts(ip, port, pa)
	}
	if err == nil {
		var tables []artnet.TodTable
		if tables, err = ctl.TodTables(ip, pa, 1+len(others), todWait); err == nil {
			var uids []artnet.UID
			if len(others) == 0 {
				uids = soloPortFixtures(ip, port, tables)
			} else {
				uids, out.Note, err = sharedPortFixtures(ip, port, others, tables)
			}
			if err == nil {
				fixtures = make([]rdmFixture, 0, len(uids))
				for _, u := range uids {
					fixtures = append(fixtures, readFixture(ctl, ip, pa, u))
				}
				out.Source = "artnet"
			}
		}
	}
	if artErr := err; artErr != nil {
		// Fall back to what the node has overheard (its own discovery, and the
		// replies to any RDM controller's requests, ours included).
		luminex.Warnf("RDM port %d on %s over Art-Net: %v; using what the node has overheard", port, ip, artErr)
		if fixtures, err = overheardFixtures(ip, port); err != nil {
			return nil, err
		}
		out.Source = "overheard"
		out.Note = "Not asked over Art-Net (" + artErr.Error() + "). This is what the node has overheard, which can be incomplete."
	}
	if fixtures == nil {
		fixtures = []rdmFixture{}
	}
	markOverlaps(fixtures)
	out.Fixtures, out.At = fixtures, time.Now()
	return out, nil
}

// sharingPorts lists the node's other DMX ports with RDM on at this port's
// port-address. An RDM request to the address goes out on all of their lines,
// and each answers an ArtTodRequest with its own table.
func sharingPorts(ip string, port int, pa artnet.PortAddress) ([]int, error) {
	ios, err := dmxIOs(ip)
	if err != nil {
		return nil, err
	}
	var others []int
	for _, io := range ios {
		p := numOr(io, "port_number", -1) + 1
		if p != port && io["rdm"] == true && numOr(io, "rdm_universe", -1) == int(pa) {
			others = append(others, p)
		}
	}
	return others, nil
}

// soloPortFixtures is the port's devices when no other port shares its address:
// every table is its own, plus anything the node lists over HTTP that the
// Art-Net table did not give, so a Read never shows fewer than the node has found.
func soloPortFixtures(ip string, port int, tables []artnet.TodTable) []artnet.UID {
	uids := mergeTables(tables)
	rest, err := connectedUIDs(ip, port)
	if err != nil {
		return uids
	}
	rdmStoreCount(ip, port, len(rest))
	seen := map[artnet.UID]bool{}
	for _, u := range uids {
		seen[u] = true
	}
	for _, s := range rest {
		if u, perr := artnet.ParseUID(s); perr == nil && !seen[u] {
			seen[u] = true
			uids = append(uids, u)
		}
	}
	return uids
}

// sharedPortFixtures picks this port's devices when other ports with RDM on
// share its address. The node sends a table per port, but which table is which
// cannot be told (see artnet.TodTables), and 1.7.0, matching tables to the
// node's list by one shared device, showed both ports' fixtures on port 10 of
// LumiNode 12 - 5, and an address went to a fixture on port 9 (19 Nov 2026). So
// the node's own list for each port decides: a device is this port's only when
// the node lists it here and on no other port at the address. A device in the
// Art-Net tables that the node lists on no port is left out and noted.
func sharedPortFixtures(ip string, port int, others []int, tables []artnet.TodTable) ([]artnet.UID, string, error) {
	lists, err := portLists(ip, append([]int{port}, others...))
	if err != nil {
		return nil, "", err
	}
	rdmStoreCount(ip, port, len(lists[port].order))
	owner := map[artnet.UID][]int{}
	for _, p := range append([]int{port}, others...) {
		for _, u := range lists[p].order {
			owner[u] = append(owner[u], p)
		}
	}
	var uids []artnet.UID
	var notes []string
	for _, u := range lists[port].order {
		if ps := owner[u]; len(ps) > 1 {
			luminex.Warnf("RDM port %d on %s: the node lists %s on ports %v; left out", port, ip, u, ps)
			notes = append(notes, fmt.Sprintf("%s is listed by the node on ports %s, so it is not shown; Rediscover each port.", u, joinPorts(ps)))
			continue
		}
		uids = append(uids, u)
	}
	unplaced := 0
	for _, u := range mergeTables(tables) {
		if len(owner[u]) == 0 {
			unplaced++
		}
	}
	luminex.Logf("RDM port %d on %s: shares its universe with port(s) %v; %d tables, %d listed here, %d on no port's list",
		port, ip, others, len(tables), len(uids), unplaced)
	if unplaced > 0 {
		notes = append(notes, fmt.Sprintf("%d more fixture(s) answered on this universe that the node has not placed on a port. Ports %s share the universe, so only fixtures the node has found on this port are shown; Rediscover to place the rest.",
			unplaced, joinPorts(append([]int{port}, others...))))
	}
	return uids, strings.Join(notes, " "), nil
}

// uidList is one port's devices as the node lists them, in its order.
type uidList struct {
	order []artnet.UID
	set   map[artnet.UID]bool
}

// portLists reads the node's device list for each port.
func portLists(ip string, ports []int) (map[int]uidList, error) {
	out := map[int]uidList{}
	for _, p := range ports {
		raw, err := connectedUIDs(ip, p)
		if err != nil {
			return nil, fmt.Errorf("the node's RDM list for port %d could not be read, and without it fixtures on ports sharing a universe cannot be told apart: %w", p, err)
		}
		l := uidList{set: map[artnet.UID]bool{}}
		for _, s := range raw {
			if u, err := artnet.ParseUID(s); err == nil && !l.set[u] {
				l.set[u] = true
				l.order = append(l.order, u)
			}
		}
		out[p] = l
	}
	return out, nil
}

func joinPorts(ps []int) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = strconv.Itoa(p)
	}
	if len(s) == 1 {
		return s[0]
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}

// checkFixturePort refuses an RDM SET to a device the node does not list on
// this port, when other ports with RDM on share the port's address: the SET
// goes out on every one of their lines, so the device that takes it could be on
// another port (1.7.0 readdressed a fixture on port 9 from port 10's tab).
func checkFixturePort(ip string, port int, pa artnet.PortAddress, u artnet.UID) error {
	others, err := sharingPorts(ip, port, pa)
	if err != nil {
		return err
	}
	if len(others) == 0 {
		return nil
	}
	lists, err := portLists(ip, append([]int{port}, others...))
	if err != nil {
		return fmt.Errorf("nothing sent: %w", err)
	}
	for _, o := range others {
		if lists[o].set[u] {
			return fmt.Errorf("nothing sent: the node lists %s on port %d, not port %d (ports %s are on the same universe, so it would answer from there); read port %d again",
				u, o, port, joinPorts(append([]int{port}, others...)), port)
		}
	}
	if !lists[port].set[u] {
		return fmt.Errorf("nothing sent: the node does not list %s on port %d, and ports %s are on the same universe, so it may be on another port; Rediscover port %d",
			u, port, joinPorts(append([]int{port}, others...)), port)
	}
	return nil
}

func mergeTables(tables []artnet.TodTable) []artnet.UID {
	seen := map[artnet.UID]bool{}
	var all []artnet.UID
	for _, t := range tables {
		for _, u := range t.UIDs {
			if !seen[u] {
				seen[u] = true
				all = append(all, u)
			}
		}
	}
	return all
}

// rdmTarget is the Art-Net port-address that reaches the fixtures on a DMX port.
func rdmTarget(ip string, port int) (artnet.PortAddress, error) {
	io, err := dmxIOFor(ip, port)
	if err != nil {
		return 0, err
	}
	if io["rdm"] != true {
		return 0, fmt.Errorf("RDM is off on DMX port %d", port)
	}
	universe := numOr(io, "rdm_universe", -1)
	if universe < 0 || universe > 0x7FFF {
		return 0, fmt.Errorf("the node gives DMX port %d no RDM universe; does the port have an input?", port)
	}
	return artnet.PortAddress(universe), nil
}

// readFixture asks one device for everything the table shows. DEVICE_INFO is
// needed; any other answer that does not come leaves its field blank.
func readFixture(ctl *artnet.Controller, ip string, pa artnet.PortAddress, u artnet.UID) rdmFixture {
	f := rdmFixture{UID: u.String(), ManufacturerID: int(u.Manufacturer())}
	d, err := ctl.Get(ip, pa, u, artnet.PIDDeviceInfo, nil)
	if err == nil {
		var info artnet.DeviceInfo
		if info, err = artnet.ParseDeviceInfo(d); err == nil {
			f.ModelID, f.Category = int(info.Model), artnet.CategoryName(info.Category)
			f.Footprint, f.Personality, f.Personalities = int(info.Footprint), int(info.Personality), int(info.PersonalityCount)
			f.Sensors, f.SubDevices = int(info.Sensors), int(info.SubDevices)
			if info.Footprint > 0 && info.StartAddress >= 1 && info.StartAddress <= 512 {
				s := int(info.StartAddress)
				f.Start = &s
			}
		}
	}
	if err != nil {
		f.Error = err.Error()
		if errors.Is(err, artnet.ErrNoReply) {
			return f // gone or not answering: don't wait on every other field too
		}
	}
	text := func(pid uint16, into *string) {
		if d, err := ctl.Get(ip, pa, u, pid, nil); err == nil {
			*into = artnet.Text(d)
		}
	}
	text(artnet.PIDManufacturerLabel, &f.Manufacturer)
	text(artnet.PIDDeviceModelDescription, &f.Model)
	text(artnet.PIDDeviceLabel, &f.Label)
	text(artnet.PIDSoftwareVersionLabel, &f.Software)
	if f.Personality > 0 {
		if d, err := ctl.Get(ip, pa, u, artnet.PIDDMXPersonalityDescription, []byte{byte(f.Personality)}); err == nil && len(d) >= 3 {
			f.PersonalityName = artnet.Text(d[3:])
		}
	}
	if d, err := ctl.Get(ip, pa, u, artnet.PIDIdentifyDevice, nil); err == nil && len(d) == 1 {
		f.Identify = d[0] == 1
	}
	return f
}

// fixtureEdit is what the RDM tab can change on one fixture. Nil fields stay as they are.
type fixtureEdit struct {
	Personality *int    `json:"personality"`
	Start       *int    `json:"start"`
	Label       *string `json:"label"`
	Identify    *bool   `json:"identify"`
}

func (e fixtureEdit) check() error {
	if e.Personality == nil && e.Start == nil && e.Label == nil && e.Identify == nil {
		return errors.New("nothing to change")
	}
	if e.Personality != nil && (*e.Personality < 1 || *e.Personality > 255) {
		return fmt.Errorf("personality %d is not 1-255", *e.Personality)
	}
	if e.Start != nil && (*e.Start < 1 || *e.Start > 512) {
		return fmt.Errorf("DMX address %d is not 1-512", *e.Start)
	}
	if e.Label != nil {
		if len(*e.Label) > 32 {
			return fmt.Errorf("a label is at most 32 characters (this one is %d)", len(*e.Label))
		}
		for _, c := range *e.Label {
			if c < 0x20 || c > 0x7E {
				return fmt.Errorf("a label takes plain ASCII letters, numbers and punctuation only, not %q", c)
			}
		}
	}
	return nil
}

// editFixture sends the RDM SETs for an edit, personality first (it can change
// the footprint), then address, label and identify, and reads the fixture back.
// The first refusal stops the rest, and the error says what did and did not go.
func editFixture(ip string, port int, uid string, e fixtureEdit) (rdmFixture, error) {
	if err := e.check(); err != nil {
		return rdmFixture{}, err
	}
	u, err := artnet.ParseUID(uid)
	if err != nil {
		return rdmFixture{}, err
	}
	pa, err := rdmTarget(ip, port)
	if err != nil {
		return rdmFixture{}, err
	}
	if err := checkFixturePort(ip, port, pa, u); err != nil {
		return rdmFixture{}, err
	}
	ctl, err := rdmController()
	if err != nil {
		return rdmFixture{}, err
	}
	type set struct {
		what string
		pid  uint16
		data []byte
	}
	var sets []set
	if e.Personality != nil {
		sets = append(sets, set{fmt.Sprintf("personality %d", *e.Personality), artnet.PIDDMXPersonality, []byte{byte(*e.Personality)}})
	}
	if e.Start != nil {
		sets = append(sets, set{fmt.Sprintf("address %d", *e.Start), artnet.PIDDMXStartAddress, []byte{byte(*e.Start >> 8), byte(*e.Start)}})
	}
	if e.Label != nil {
		sets = append(sets, set{fmt.Sprintf("label %q", *e.Label), artnet.PIDDeviceLabel, []byte(*e.Label)})
	}
	if e.Identify != nil {
		on := byte(0)
		if *e.Identify {
			on = 1
		}
		sets = append(sets, set{map[bool]string{true: "identify on", false: "identify off"}[*e.Identify], artnet.PIDIdentifyDevice, []byte{on}})
	}
	var done []string
	for _, x := range sets {
		err := ctl.Set(ip, pa, u, x.pid, x.data)
		if errors.Is(err, luminex.ErrDryRun) {
			continue
		}
		if err != nil {
			msg := fmt.Sprintf("%s: %v", x.what, err)
			if len(done) > 0 {
				msg += " (already set: " + strings.Join(done, ", ") + ")"
			}
			return rdmFixture{}, errors.New(msg)
		}
		done = append(done, x.what)
	}
	// Identify alone is read back on its own: Follow ID sends one on every tile
	// change, and a whole read is eight requests.
	if e.Identify != nil && e.Personality == nil && e.Start == nil && e.Label == nil {
		f := rdmFixture{UID: u.String(), Identify: *e.Identify, Partial: true}
		if luminex.DryRun {
			f.Identify = !*e.Identify // not sent: the fixture is as it was
		}
		d, err := ctl.Get(ip, pa, u, artnet.PIDIdentifyDevice, nil)
		if err != nil || len(d) != 1 {
			return f, fmt.Errorf("identify sent, but reading it back failed: %v", err)
		}
		f.Identify = d[0] == 1
		if !luminex.DryRun && f.Identify != *e.Identify {
			return f, fmt.Errorf("the fixture accepted identify %v but reads %v", *e.Identify, f.Identify)
		}
		return f, nil
	}
	f := readFixture(ctl, ip, pa, u)
	if f.Error != "" {
		return f, fmt.Errorf("changed, but reading it back failed: %s", f.Error)
	}
	if !luminex.DryRun { // check the fixture took what was sent
		var off []string
		if e.Personality != nil && f.Personality != *e.Personality {
			off = append(off, fmt.Sprintf("personality reads %d", f.Personality))
		}
		if e.Start != nil && f.Footprint > 0 && (f.Start == nil || *f.Start != *e.Start) {
			off = append(off, "address reads "+startText(f.Start))
		}
		if e.Label != nil && f.Label != *e.Label {
			off = append(off, fmt.Sprintf("label reads %q", f.Label))
		}
		if len(off) > 0 {
			return f, fmt.Errorf("the fixture accepted the change but %s", strings.Join(off, ", "))
		}
	}
	return f, nil
}

func startText(s *int) string {
	if s == nil {
		return "none"
	}
	return strconv.Itoa(*s)
}

// rdmPersonality is one entry of a fixture's personality list.
type rdmPersonality struct {
	Number    int    `json:"number"`
	Footprint int    `json:"footprint"`
	Name      string `json:"name"`
}

// fixturePersonalities reads every personality's description (one GET each).
func fixturePersonalities(ip string, port int, uid string) ([]rdmPersonality, error) {
	u, err := artnet.ParseUID(uid)
	if err != nil {
		return nil, err
	}
	pa, err := rdmTarget(ip, port)
	if err != nil {
		return nil, err
	}
	ctl, err := rdmController()
	if err != nil {
		return nil, err
	}
	d, err := ctl.Get(ip, pa, u, artnet.PIDDeviceInfo, nil)
	if err != nil {
		return nil, err
	}
	info, err := artnet.ParseDeviceInfo(d)
	if err != nil {
		return nil, err
	}
	out := []rdmPersonality{}
	for n := 1; n <= int(info.PersonalityCount); n++ {
		p := rdmPersonality{Number: n}
		if d, err := ctl.Get(ip, pa, u, artnet.PIDDMXPersonalityDescription, []byte{byte(n)}); err == nil && len(d) >= 3 {
			p.Footprint, p.Name = int(d[1])<<8|int(d[2]), artnet.Text(d[3:])
		}
		out = append(out, p)
	}
	return out, nil
}

// overheardFixtures shapes the node's GET /api/rdm/uid for one port.
func overheardFixtures(ip string, port int) ([]rdmFixture, error) {
	raw, err := lumiGet(ip, "/api/rdm/uid?port="+strconv.Itoa(port-1))
	if err != nil {
		return nil, err
	}
	var list []obj
	if raw != nil && json.Unmarshal(raw, &list) != nil {
		var one obj // the node may answer a single object
		if json.Unmarshal(raw, &one) != nil {
			return nil, fmt.Errorf("bad JSON from /api/rdm/uid")
		}
		list = []obj{one}
	}
	out := []rdmFixture{}
	for _, d := range list {
		if numOr(d, "port", port-1) != port-1 {
			continue
		}
		f := rdmFixture{UID: str(d, "uid", ""), Manufacturer: str(d, "manufacturer_label", ""), ManufacturerID: numOr(d, "manufacturer", 0),
			Model: str(d, "device_model_description", ""), ModelID: numOr(d, "device_model", 0), Label: str(d, "device_label", ""),
			Software: str(d, "software_version_label", ""), Footprint: numOr(d, "dmx_footprint", 0), Personality: numOr(d, "dmx_personality", 0),
			Personalities: numOr(d, "personality_count", 0), Sensors: numOr(d, "sensor_count", 0), SubDevices: numOr(d, "sub_device_count", 0),
			Category: artnet.CategoryName(uint16(numOr(d, "product_category", 0)))}
		// A UID that is not one cannot be addressed, and the page puts UIDs into
		// its buttons, so anything else a device sends here is left out.
		u, err := artnet.ParseUID(f.UID)
		if err != nil {
			continue
		}
		f.UID = u.String()
		if s := numOr(d, "dmx_start_address", -1); s >= 1 && s <= 512 && f.Footprint > 0 {
			f.Start = &s
		}
		if names, ok := d["dmx_personality_description"].([]any); ok && f.Personality >= 1 && f.Personality <= len(names) {
			f.PersonalityName = fmt.Sprint(names[f.Personality-1])
		}
		out = append(out, f)
	}
	return out, nil
}

// markOverlaps sorts by address and notes devices that share channels or run past 512.
func markOverlaps(fx []rdmFixture) {
	sort.SliceStable(fx, func(i, j int) bool {
		a, b := fx[i].Start, fx[j].Start
		if (a == nil) != (b == nil) {
			return a != nil
		}
		if a != nil && *a != *b {
			return *a < *b
		}
		return fx[i].UID < fx[j].UID
	})
	end := func(f rdmFixture) int { return *f.Start + f.Footprint - 1 }
	for i := range fx {
		fx[i].Overlaps = nil
		if fx[i].Start == nil {
			continue
		}
		fx[i].PastEnd = end(fx[i]) > 512
		for j := range fx {
			if i != j && fx[j].Start != nil && *fx[i].Start <= end(fx[j]) && *fx[j].Start <= end(fx[i]) {
				fx[i].Overlaps = append(fx[i].Overlaps, fx[j].UID)
			}
		}
	}
}

func artnetState() obj {
	ctl, err := rdmController()
	if err != nil {
		return obj{"ok": false, "error": err.Error()}
	}
	return obj{"ok": true, "uid": ctl.UID().String()}
}

func portParam(w http.ResponseWriter, r *http.Request) int {
	p, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || p < 1 || p > 64 {
		fail(w, 400, fmt.Errorf("bad DMX port %q", r.PathValue("port")))
		return 0
	}
	return p
}

func registerRDMRoutes(handle func(string, http.HandlerFunc)) {
	registerRDMParamRoutes(handle)
	handle("GET /api/node/{ip}/rdm", func(w http.ResponseWriter, r *http.Request) {
		ip := ipParam(w, r)
		if ip == "" {
			return
		}
		ports, err := rdmPorts(ip)
		if err != nil {
			deviceFail(w, "RDM ports "+ip, err)
			return
		}
		rdmStorePorts(ip, ports)
		ok(w, obj{"ports": ports, "artnet": artnetState(), "dryRun": luminex.DryRun, "state": rdmSnapshot(ip)})
	})

	handle("PUT /api/node/{ip}/rdm/{port}", func(w http.ResponseWriter, r *http.Request) {
		ip, port := ipParam(w, r), 0
		if ip == "" {
			return
		}
		if port = portParam(w, r); port == 0 {
			return
		}
		var body struct {
			On *bool `json:"on"`
		}
		if err := decodeBody(r, &body); err != nil || body.On == nil {
			fail(w, 400, errors.New(`send {"on": true} or {"on": false}`))
			return
		}
		rdmStoreBusy(ip, port, map[bool]string{true: "Turning RDM on…", false: "Turning RDM off…"}[*body.On])
		err := setPortRDM(ip, port, *body.On)
		rdmStoreBusy(ip, port, "")
		if err != nil {
			deviceFail(w, "RDM on/off "+ip, err)
			return
		}
		ports, err := rdmPorts(ip)
		if err != nil {
			deviceFail(w, "RDM ports "+ip, err)
			return
		}
		rdmStorePorts(ip, ports)
		ok(w, obj{"ports": ports, "artnet": artnetState(), "dryRun": luminex.DryRun})
	})

	// Which fixtures the node has found on a port so far: the page asks this while
	// the node searches and reads once the list has changed and settled, since a
	// search takes several seconds (9-14 s for 14-18 fixtures on a real line).
	handle("GET /api/node/{ip}/rdm/{port}/found", func(w http.ResponseWriter, r *http.Request) {
		ip, port := ipParam(w, r), 0
		if ip == "" {
			return
		}
		if port = portParam(w, r); port == 0 {
			return
		}
		uids, err := connectedUIDs(ip, port)
		if err != nil {
			deviceFail(w, "RDM found "+ip, err)
			return
		}
		sort.Strings(uids)
		rdmStoreCount(ip, port, len(uids))
		ok(w, obj{"count": len(uids), "uids": uids})
	})

	handle("POST /api/node/{ip}/rdm/{port}/discovery", func(w http.ResponseWriter, r *http.Request) {
		ip, port := ipParam(w, r), 0
		if ip == "" {
			return
		}
		if port = portParam(w, r); port == 0 {
			return
		}
		// The node wants a JSON body, even an empty one: without it a LumiNode 12 (v2.9.1) answers 415.
		if _, err := lumiDo("POST", ip, fmt.Sprintf("/api/dmx/ports/%d/force_discovery", port-1), obj{}); err != nil {
			deviceFail(w, "RDM discovery "+ip, err)
			return
		}
		ok(w, nil)
	})

	handle("POST /api/node/{ip}/rdm/{port}/fixture/{uid}", func(w http.ResponseWriter, r *http.Request) {
		ip, port := ipParam(w, r), 0
		if ip == "" {
			return
		}
		if port = portParam(w, r); port == 0 {
			return
		}
		var e fixtureEdit
		if err := decodeBody(r, &e); err != nil {
			fail(w, 400, fmt.Errorf("bad fixture edit: %v", err))
			return
		}
		if err := e.check(); err != nil {
			fail(w, 400, err)
			return
		}
		f, err := editFixture(ip, port, r.PathValue("uid"), e)
		if err != nil {
			deviceFail(w, "RDM edit "+ip, err)
			return
		}
		stored := f
		if luminex.DryRun && e.Identify != nil && f.Partial {
			stored.Identify = *e.Identify // not sent: shown as asked, as the page always did, so Follow ID settles
		}
		rdmStoreFixture(ip, port, stored)
		ok(w, obj{"fixture": f, "dryRun": luminex.DryRun})
	})

	handle("GET /api/node/{ip}/rdm/{port}/fixture/{uid}/personalities", func(w http.ResponseWriter, r *http.Request) {
		ip, port := ipParam(w, r), 0
		if ip == "" {
			return
		}
		if port = portParam(w, r); port == 0 {
			return
		}
		list, err := fixturePersonalities(ip, port, r.PathValue("uid"))
		if err != nil {
			deviceFail(w, "RDM personalities "+ip, err)
			return
		}
		ok(w, obj{"personalities": list})
	})

	handle("GET /api/node/{ip}/rdm/{port}/fixtures", func(w http.ResponseWriter, r *http.Request) {
		ip, port := ipParam(w, r), 0
		if ip == "" {
			return
		}
		if port = portParam(w, r); port == 0 {
			return
		}
		rdmStoreBusy(ip, port, "Reading…")
		res, err := readFixtures(ip, port)
		rdmStoreBusy(ip, port, "")
		if err != nil {
			deviceFail(w, "RDM fixtures "+ip, err)
			return
		}
		rdmStoreRead(ip, res)
		out := obj{"port": res.Port, "universe": res.Universe, "source": res.Source, "at": res.At, "fixtures": res.Fixtures}
		if res.Note != "" {
			out["note"] = res.Note
		}
		ok(w, out)
	})
}
