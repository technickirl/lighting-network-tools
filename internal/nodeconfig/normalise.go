package nodeconfig

// Maps raw LumiNode API objects to the flat shapes public/index.html renders.
// Field names must stay in step with the UI (they are the v28 Node contract).

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

func str(m obj, key, def string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return def
}

func num(m obj, key string) (int, bool) {
	if v, ok := m[key]; ok {
		if f, ok := v.(float64); ok {
			return int(f), true
		}
	}
	return 0, false
}

func numOr(m obj, key string, def int) int {
	if n, ok := num(m, key); ok {
		return n
	}
	return def
}

func boolean(m obj, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func boolOr(m obj, key string, def bool) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// numOrNull returns the number or nil (JSON null), like the Node `!== undefined ? x : null`.
func numOrNull(m obj, key string) any {
	if n, ok := num(m, key); ok {
		return n
	}
	return nil
}

type DeviceInfo struct {
	Model         string `json:"model"`
	ShortName     string `json:"shortName"`
	LongName      string `json:"longName"`
	Serial        string `json:"serial"`
	MAC           string `json:"mac"`
	DmxPorts      int    `json:"dmxPorts"`
	ProcessBlocks int    `json:"processBlocks"`
	DefaultIP     string `json:"defaultIp"`
	Uptime        string `json:"uptime"`
	AuthEnabled   bool   `json:"authEnabled"`
}

func normaliseDevice(d obj, uptime *float64) DeviceInfo {
	auth := false
	if a, ok := d["auth"].(map[string]any); ok {
		auth = boolean(a, "web_auth_enabled")
	}
	return DeviceInfo{
		Model:         str(d, "type", "LumiNode"),
		ShortName:     str(d, "short_name", "—"),
		LongName:      str(d, "long_name", "—"),
		Serial:        str(d, "serial", "—"),
		MAC:           str(d, "mac_address", "—"),
		DmxPorts:      numOr(d, "nr_dmx_ports", 0),
		ProcessBlocks: numOr(d, "nr_processblocks", 0),
		DefaultIP:     str(d, "default_ip", "—"),
		Uptime:        formatUptime(uptime),
		AuthEnabled:   auth,
	}
}

// IORef is one end of a process block's wiring: an IO id resolved into words.
// Label says what the IO is ("sACN U1", "DMX Port 1", "Internal PB 1"); Name is
// the device-given name ("" when none); Detail carries the one or two settings
// an operator wants next to it ("prio 100", "RDM"). Missing is set when the id
// is not in the IO list (the block points at an IO that no longer exists).
type IORef struct {
	ID      int    `json:"id"`
	Label   string `json:"label"`
	Name    string `json:"name"`
	IoClass string `json:"ioClass"`
	IoType  string `json:"ioType"`
	Detail  string `json:"detail"`
	Missing bool   `json:"missing,omitempty"`
}

// Text is the one-line form used in flows and logs: the name when the IO has
// one that adds information, then the label, then the detail in brackets.
func (r IORef) Text() string {
	s := r.Label
	if r.Name != "" && r.Name != r.Label && !strings.HasSuffix(r.Label, r.Name) {
		s = r.Name + " · " + r.Label
	}
	if r.Detail != "" {
		s += " (" + r.Detail + ")"
	}
	return s
}

// PBInput is one input slot of a process block (slots are 0-based on the
// device; the page shows them 1-based). Active comes from
// summarized_active_input (BACKUP/SWITCH only). Sources lists the senders the
// node currently sees on that slot, from sources.inputs[slot].
type PBInput struct {
	Slot    int      `json:"slot"`
	IO      IORef    `json:"io"`
	Active  bool     `json:"active"`
	Sources []string `json:"sources"`
}

// PBControl is a control input (backup_ctrl, switch_ctrl, xfade_ctrl,
// masterlimit_ctrl) that is set, i.e. not -1.
type PBControl struct {
	Role string `json:"role"`
	IO   IORef  `json:"io"`
}

type ProcessBlock struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	InputIDs  []int  `json:"inputIds"`
	OutputIDs []int  `json:"outputIds"`
	Backup    bool   `json:"backup"`
	Timeout   int    `json:"timeout"`
	Ratelimit bool   `json:"ratelimit"`
	// Resolved view (added for the Process page; the fields above are the
	// v28 contract that Set Input, Quick Setup and Startup still read).
	Inputs         []PBInput   `json:"inputs"`
	Outputs        []IORef     `json:"outputs"`
	Controls       []PBControl `json:"controls"`
	ActiveInput    any         `json:"activeInput"` // slot index or null
	Masterlimit    string      `json:"masterlimit"`
	Color          string      `json:"color"`
	StartupBuffer  int         `json:"startupBuffer"` // channels stored, 0 = none
	TakeoverActive bool        `json:"takeoverActive"`
	Configured     bool        `json:"configured"` // mode set or any IO attached
	Flow           string      `json:"-"`          // "sACN U1 (prio 100) → DMX Port 1", for logs and tests; the page builds its own
}

// mapValues returns the numeric values of a {key: n} object, sorted by key
// (inputs are {slot: io_id}; the v28 code read the values of outputs too).
func mapValues(v any) []int {
	out := []int{}
	m, ok := v.(map[string]any)
	if !ok {
		return out
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if f, ok := m[k].(float64); ok {
			out = append(out, int(f))
		}
	}
	return out
}

// mapKeys returns the numeric keys of a {key: v} object, sorted.
func mapKeys(v any) []int {
	out := []int{}
	m, ok := v.(map[string]any)
	if !ok {
		return out
	}
	for k := range m {
		if n, err := strconv.Atoi(k); err == nil {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// ioIndex maps IO id → raw IO object.
func ioIndex(ios []obj) map[int]obj {
	idx := make(map[int]obj, len(ios))
	for _, io := range ios {
		if id, ok := num(io, "id"); ok {
			idx[id] = io
		}
	}
	return idx
}

// describeIO turns a raw IO into an IORef. The id namespaces seen on a real
// LumiNode 12 (v2.9.1): sACN/Art-Net IOs are small ints, DMX port IOs are
// 100000+port ("Port 1", port_number 0), internal IOs are 101000+pb.
func describeIO(io obj) IORef {
	id := numOr(io, "id", -1)
	r := IORef{ID: id, Name: str(io, "name", ""), IoClass: str(io, "io_class", ""), IoType: str(io, "io_type", "")}
	var details []string
	switch r.IoClass {
	case "sacn":
		r.Label = fmt.Sprintf("sACN U%d", numOr(io, "universe", 0))
		if p, ok := num(io, "priority"); ok {
			details = append(details, fmt.Sprintf("prio %d", p))
		}
		if g := numOr(io, "group", 0); g > 0 {
			details = append(details, fmt.Sprintf("group %d", g))
		}
	case "artnet":
		r.Label = fmt.Sprintf("Art-Net U%d", numOr(io, "universe", 0))
		if g := numOr(io, "group", 0); g > 0 {
			details = append(details, fmt.Sprintf("group %d", g))
		}
	case "dmx":
		if port, ok := num(io, "port_number"); ok {
			r.Label = fmt.Sprintf("DMX Port %d", port+1)
		} else {
			r.Label = fmt.Sprintf("DMX IO %d", id)
		}
		if boolean(io, "rdm") {
			details = append(details, "RDM")
		}
	case "internal":
		r.Label = fmt.Sprintf("Internal PB %d", numOr(io, "pb_number", -1)+1)
	case "":
		r.Label = fmt.Sprintf("IO %d", id)
	default:
		r.Label = fmt.Sprintf("%s IO %d", r.IoClass, id)
		if u, ok := num(io, "universe"); ok {
			r.Label = fmt.Sprintf("%s U%d", r.IoClass, u)
		}
	}
	r.Detail = strings.Join(details, ", ")
	return r
}

// refFor resolves an IO id through the index; unknown ids come back as
// "IO n" with Missing set rather than being dropped.
func refFor(idx map[int]obj, id int) IORef {
	if io, ok := idx[id]; ok {
		return describeIO(io)
	}
	return IORef{ID: id, Label: fmt.Sprintf("IO %d", id), Missing: true}
}

// sourceTexts flattens sources.inputs[slot] (null, one sender or a list of
// senders) into "ip (name, prio N)" strings.
func sourceTexts(v any) []string {
	out := []string{}
	one := func(m obj) {
		ip := str(m, "ip", "")
		if ip == "" {
			return
		}
		var extra []string
		if n := str(m, "name", ""); n != "" {
			extra = append(extra, n)
		}
		if p, ok := num(m, "priority"); ok {
			extra = append(extra, fmt.Sprintf("prio %d", p))
		}
		if len(extra) > 0 {
			ip += " (" + strings.Join(extra, ", ") + ")"
		}
		out = append(out, ip)
	}
	switch x := v.(type) {
	case map[string]any:
		one(x)
	case []any:
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				one(m)
			}
		}
	}
	return out
}

var controlRoles = []struct{ key, role string }{
	{"backup_ctrl", "backup"}, {"switch_ctrl", "switch"}, {"xfade_ctrl", "xfade"}, {"masterlimit_ctrl", "masterlimit"},
}

func normaliseProcessBlock(pb obj, idx map[int]obj) ProcessBlock {
	id := numOr(pb, "id", -1)
	b := ProcessBlock{
		ID:          id,
		Name:        str(pb, "name", fmt.Sprintf("Process Block %d", id)),
		Mode:        upper(str(pb, "mode", "NONE")),
		InputIDs:    mapValues(pb["inputs"]),
		OutputIDs:   mapValues(pb["outputs"]),
		Backup:      boolean(pb, "backup_auto_recover"),
		Timeout:     numOr(pb, "timeout", 0),
		Ratelimit:   boolOr(pb, "ratelimiting", true),
		Inputs:      []PBInput{},
		Outputs:     []IORef{},
		Controls:    []PBControl{},
		ActiveInput: numOrNull(pb, "summarized_active_input"),
		Masterlimit: upper(str(pb, "masterlimit", "NONE")),
	}
	if cs, ok := pb["colors"].([]any); ok && len(cs) > 0 {
		if c, ok := cs[0].(string); ok {
			b.Color = c
		}
	}
	if sb, ok := pb["startup_buffer"].([]any); ok {
		b.StartupBuffer = len(sb)
	}
	b.TakeoverActive = boolean(pb, "takeover_active")

	var slotSources []any
	if s, ok := pb["sources"].(map[string]any); ok {
		slotSources, _ = s["inputs"].([]any)
	}
	inputs, _ := pb["inputs"].(map[string]any)
	for _, slot := range mapKeys(pb["inputs"]) {
		f, _ := inputs[strconv.Itoa(slot)].(float64)
		in := PBInput{Slot: slot, IO: refFor(idx, int(f)), Sources: []string{}}
		if a, ok := b.ActiveInput.(int); ok && a == slot {
			in.Active = true
		}
		if slot < len(slotSources) {
			in.Sources = sourceTexts(slotSources[slot])
		}
		b.Inputs = append(b.Inputs, in)
	}
	for _, oid := range b.OutputIDs {
		b.Outputs = append(b.Outputs, refFor(idx, oid))
	}
	for _, c := range controlRoles {
		if cid, ok := num(pb, c.key); ok && cid >= 0 {
			b.Controls = append(b.Controls, PBControl{Role: c.role, IO: refFor(idx, cid)})
		}
	}
	b.Configured = b.Mode != "NONE" || len(b.Inputs) > 0 || len(b.Outputs) > 0
	b.Flow = flowText(b)
	return b
}

// flowText is the one-line summary: inputs → outputs, the active input marked.
func flowText(b ProcessBlock) string {
	var ins, outs []string
	for _, in := range b.Inputs {
		t := in.IO.Text()
		if in.Active {
			t = "● " + t
		}
		ins = append(ins, t)
	}
	for _, o := range b.Outputs {
		outs = append(outs, o.Text())
	}
	left, right := "no input", "no output"
	if len(ins) > 0 {
		left = strings.Join(ins, " + ")
	}
	if len(outs) > 0 {
		right = strings.Join(outs, ", ")
	}
	if len(ins) == 0 && len(outs) == 0 {
		return ""
	}
	return left + " → " + right
}

// normaliseProcessBlocks resolves every block's IO ids through ios (nil when
// the IO list could not be fetched: ids then show as "IO n").
func normaliseProcessBlocks(list []obj, ios []obj) []ProcessBlock {
	idx := ioIndex(ios)
	out := make([]ProcessBlock, 0, len(list))
	for _, pb := range list {
		out = append(out, normaliseProcessBlock(pb, idx))
	}
	return out
}

// IOUse says where an IO is attached: which process block, as what.
// Role is "input" (Slot set, 0-based), "output" or a control role.
type IOUse struct {
	PB     int    `json:"pb"`
	PBName string `json:"pbName"`
	Role   string `json:"role"`
	Slot   int    `json:"slot"`
}

type IO struct {
	ID                 int    `json:"id"`
	Name               string `json:"name"`
	IoClass            string `json:"ioClass"`
	IoType             string `json:"ioType"`
	PortNumber         any    `json:"portNumber"`
	RDM                string `json:"rdm"`
	RDMUniverse        any    `json:"rdmUniverse"`
	Universe           any    `json:"universe"`
	Priority           any    `json:"priority"`
	IP                 any    `json:"ip"` // source_ip (v2.9.1) or ip (v2.7.2); "0.0.0.0" = any sender
	ArtnetUniverse     any    `json:"artnetUniverse"`
	Backup             bool   `json:"backup"`
	Follow             bool   `json:"follow"`
	AdaptiveDiscovery  bool   `json:"adaptiveDiscovery"`
	StreamActivityTime int    `json:"streamActivityTime"`
	// Added for the IOs page.
	Label              string  `json:"label"` // "sACN U1", "DMX Port 1", "Internal PB 1"
	Group              any     `json:"group"` // network IOs; null elsewhere
	TakeOwnIP          any     `json:"takeOwnIp"`
	PerChannelPriority bool    `json:"perChannelPriority"`
	Interweaving       bool    `json:"interweaving"`
	DuplicateIOs       []int   `json:"duplicateIos"` // ids with the same configuration (device-reported)
	PBNumber           any     `json:"pbNumber"`     // internal IOs only
	UsedBy             []IOUse `json:"usedBy"`
	InUse              bool    `json:"inUse"`
}

func normaliseIO(io obj) IO {
	id := numOr(io, "id", -1)
	n := IO{
		ID:                 id,
		Name:               str(io, "name", ""),
		IoClass:            str(io, "io_class", "—"),
		IoType:             str(io, "io_type", "—"),
		PortNumber:         numOrNull(io, "port_number"),
		RDM:                "—",
		RDMUniverse:        "—",
		Universe:           numOrNull(io, "universe"),
		Priority:           numOrNull(io, "priority"),
		IP:                 nil,
		ArtnetUniverse:     numOrNull(io, "universe"),
		Backup:             boolean(io, "backup"),
		Follow:             boolean(io, "follow"),
		AdaptiveDiscovery:  boolean(io, "adaptive_discovery"),
		StreamActivityTime: numOr(io, "stream_activity_time", 800),
		Label:              describeIO(io).Label,
		Group:              numOrNull(io, "group"),
		PerChannelPriority: boolean(io, "per_channel_priority"),
		Interweaving:       boolean(io, "interweaving"),
		DuplicateIOs:       []int{},
		PBNumber:           numOrNull(io, "pb_number"),
		UsedBy:             []IOUse{},
	}
	if n.Name == "" {
		n.Name = n.Label // the v28 page expects a name; the device leaves sACN/internal names blank
	}
	if v, ok := io["take_own_ip"].(bool); ok {
		n.TakeOwnIP = v
	}
	if v, ok := io["rdm"].(bool); ok {
		if v {
			n.RDM = "ON"
		} else {
			n.RDM = "OFF"
		}
	}
	if u, ok := num(io, "rdm_universe"); ok && u >= 0 {
		n.RDMUniverse = u
	}
	if s := str(io, "source_ip", str(io, "ip", "")); s != "" {
		n.IP = s
	}
	if d, ok := io["duplicate_ios"].([]any); ok {
		for _, e := range d {
			if f, ok := e.(float64); ok {
				n.DuplicateIOs = append(n.DuplicateIOs, int(f))
			}
		}
	}
	return n
}

// normaliseIOs shapes the IO list and marks where each IO is attached, derived
// from the blocks' inputs (by slot), outputs and control ids.
func normaliseIOs(list []obj, blocks []obj) []IO {
	uses := map[int][]IOUse{}
	for _, pb := range blocks {
		id := numOr(pb, "id", -1)
		name := str(pb, "name", fmt.Sprintf("Process Block %d", id))
		if inputs, ok := pb["inputs"].(map[string]any); ok {
			for _, slot := range mapKeys(pb["inputs"]) {
				if f, ok := inputs[strconv.Itoa(slot)].(float64); ok {
					uses[int(f)] = append(uses[int(f)], IOUse{PB: id, PBName: name, Role: "input", Slot: slot})
				}
			}
		}
		for _, oid := range mapValues(pb["outputs"]) {
			uses[oid] = append(uses[oid], IOUse{PB: id, PBName: name, Role: "output"})
		}
		for _, c := range controlRoles {
			if cid, ok := num(pb, c.key); ok && cid >= 0 {
				uses[cid] = append(uses[cid], IOUse{PB: id, PBName: name, Role: c.role})
			}
		}
	}
	out := make([]IO, 0, len(list))
	for _, io := range list {
		n := normaliseIO(io)
		if u := uses[n.ID]; u != nil {
			n.UsedBy = u
			n.InUse = true
		}
		out = append(out, n)
	}
	return out
}

func filterClass(list []obj, class string) []obj {
	var out []obj
	for _, io := range list {
		if str(io, "io_class", "") == class {
			out = append(out, io)
		}
	}
	return out
}

type DmxPort struct {
	PortNumber     int    `json:"portNumber"`
	Broken         bool   `json:"broken"`
	ActivityState  string `json:"activityState"`
	BackupActive   bool   `json:"backupActive"`
	InputFrames    int    `json:"inputFrames"`
	OutputFrames   int    `json:"outputFrames"`
	PipelineFrames int    `json:"pipelineFrames"`
}

func normaliseDmxPorts(list []obj) []DmxPort {
	out := make([]DmxPort, 0, len(list))
	for i, p := range list {
		d := DmxPort{
			PortNumber:    i,
			Broken:        boolean(p, "broken"),
			ActivityState: str(p, "stream_activity_state", "unknown"),
			BackupActive:  boolean(p, "backup_active_dmx_tx"),
		}
		if c, ok := p["counters"].(map[string]any); ok {
			d.InputFrames = numOr(c, "input_frames", 0)
			d.OutputFrames = numOr(c, "output_frames", 0)
			d.PipelineFrames = numOr(c, "pipeline_frames", 0)
		}
		out = append(out, d)
	}
	return out
}

type Network struct {
	IP          string `json:"ip"`
	Mask        string `json:"mask"`
	Gateway     string `json:"gateway"`
	Broadcast   string `json:"broadcast"`
	BasicConfig bool   `json:"basicConfig"`
}

func normaliseNetwork(d obj) Network {
	return Network{
		IP:          str(d, "ipaddress", "—"),
		Mask:        str(d, "netmask", "—"),
		Gateway:     str(d, "gateway", "—"),
		Broadcast:   str(d, "broadcast", "—"),
		BasicConfig: boolean(d, "is_basic_config"),
	}
}

type Profile struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func normaliseProfiles(list []obj) []Profile {
	out := []Profile{}
	for _, p := range list {
		if !boolean(p, "exists") {
			continue
		}
		id := numOr(p, "id", -1)
		out = append(out, Profile{ID: id, Name: str(p, "name", fmt.Sprintf("Profile %d", id))})
	}
	return out
}

func formatUptime(s *float64) string {
	if s == nil || math.IsNaN(*s) {
		return "—"
	}
	t := int(*s)
	return fmt.Sprintf("%dd %dh %dm", t/86400, (t%86400)/3600, (t%3600)/60)
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}
