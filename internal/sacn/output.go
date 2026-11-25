package sacn

// The sACN controller behind the "sACN control" view: fixture profiles, the
// patch (which fixture sits on which universe and address) and the API that sets
// levels on the sender.
//
// A profile is a name and a list of channels, each with a type that tells the
// page how to draw its fader. A few plain ones are built in; the operator's own
// are kept in sacn-profiles.json in the user's config folder, and the patch in
// sacn-patch.json beside it, so an iPad and the laptop see the same rig.
// Levels are not saved: every run starts at zero with the output off.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

// ChannelTypes are what a profile's channel can be. The page colours a fader by it,
// and "dimmer" is the one a fixture's tile shows first.
var ChannelTypes = []string{"dimmer", "red", "green", "blue", "white", "amber", "lime", "uv", "warm", "cool", "cyan", "magenta", "yellow", "cct", "pan", "tilt", "zoom", "strobe", "other"}

// Channel is one DMX channel of a profile.
type Channel struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default int    `json:"default"` // the level it sits at until moved, 0-255
	// Highlight is the level Highlight fades the channel to, 0-255, or -1 to leave
	// it where it is. A profile saved without one gets its type's usual level.
	Highlight *int `json:"highlight,omitempty"`
	// Fine marks the low half of a 16-bit pair: the channel before it is the high
	// (coarse) half, and the page draws the two as one fader. It takes that
	// channel's type, and its highlight level follows it.
	Fine bool `json:"fine,omitempty"`
}

// fineHighlight is the highlight level of a fine channel, from its coarse one's:
// full stays full (255 and 255), anything else is exact on the coarse half alone.
func fineHighlight(coarse int) int {
	switch {
	case coarse < 0:
		return -1
	case coarse == 255:
		return 255
	}
	return 0
}

// HighlightFade is how long Highlight takes to fade in and out.
var HighlightFade = 500 * time.Millisecond

// defaultHighlights fills in the channels that have no highlight level: dimmer at
// 50%, red, green and blue at full and cyan, magenta and yellow at zero, so a
// fixture shows up white without blinding anyone. A profile with no red, green or blue brings its whites up instead.
func defaultHighlights(cs []Channel) {
	rgb := false
	for _, c := range cs {
		rgb = rgb || c.Type == "red" || c.Type == "green" || c.Type == "blue"
	}
	for i := range cs {
		if cs[i].Fine && i > 0 {
			v := fineHighlight(*cs[i-1].Highlight)
			cs[i].Highlight = &v
			continue
		}
		if cs[i].Highlight != nil {
			continue
		}
		v := -1
		switch cs[i].Type {
		case "dimmer":
			v = 128
		case "red", "green", "blue":
			v = 255
		case "cyan", "magenta", "yellow":
			v = 0 // subtractive: zero is open white
		case "white", "warm", "cool":
			if !rgb {
				v = 255
			}
		}
		cs[i].Highlight = &v
	}
}

// Profile is a kind of fixture: its channels in DMX order.
type Profile struct {
	Name     string    `json:"name"`
	Channels []Channel `json:"channels"`
	BuiltIn  bool      `json:"builtIn,omitempty"`
}

func chans(types ...string) []Channel {
	out := make([]Channel, len(types))
	for i, t := range types {
		out[i] = Channel{Name: strings.ToUpper(t[:1]) + t[1:], Type: t}
	}
	defaultHighlights(out)
	return out
}

var builtIn = []Profile{
	{Name: "Dimmer", Channels: chans("dimmer"), BuiltIn: true},
	{Name: "Dimmer + CCT", Channels: chans("dimmer", "cct"), BuiltIn: true},
	{Name: "Dimmer + RGB", Channels: chans("dimmer", "red", "green", "blue"), BuiltIn: true},
	{Name: "Dimmer + RGBW", Channels: chans("dimmer", "red", "green", "blue", "white"), BuiltIn: true},
	{Name: "RGB", Channels: chans("red", "green", "blue"), BuiltIn: true},
	{Name: "RGBA", Channels: chans("red", "green", "blue", "amber"), BuiltIn: true},
	{Name: "RGBW", Channels: chans("red", "green", "blue", "white"), BuiltIn: true},
	{Name: "RGBAW", Channels: chans("red", "green", "blue", "amber", "white"), BuiltIn: true},
	{Name: "Warm + Cool white", Channels: chans("warm", "cool"), BuiltIn: true},
}

func (p *Profile) validate() error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 80 {
		return fmt.Errorf("a profile needs a name of up to 80 characters")
	}
	if len(p.Channels) < 1 || len(p.Channels) > 512 {
		return fmt.Errorf("a profile has 1 to 512 channels")
	}
	for i := range p.Channels {
		c := &p.Channels[i]
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" {
			c.Name = "Ch " + strconv.Itoa(i+1)
		}
		if len(c.Name) > 40 {
			return fmt.Errorf("channel %d: the name is longer than 40 characters", i+1)
		}
		if c.Fine {
			if i == 0 {
				return fmt.Errorf("channel 1 cannot be a fine channel: a fine channel comes straight after the channel it belongs to")
			}
			if p.Channels[i-1].Fine {
				return fmt.Errorf("channel %d: the channel before it is a fine channel too; a fine channel comes straight after its coarse one", i+1)
			}
			c.Type = p.Channels[i-1].Type
		}
		ok := false
		for _, t := range ChannelTypes {
			ok = ok || t == c.Type
		}
		if !ok {
			return fmt.Errorf("channel %d: %q is not a channel type (%s)", i+1, c.Type, strings.Join(ChannelTypes, ", "))
		}
		if c.Default < 0 || c.Default > 255 {
			return fmt.Errorf("channel %d: the default level runs from 0 to 255", i+1)
		}
	}
	defaultHighlights(p.Channels)
	for i, c := range p.Channels {
		if *c.Highlight < -1 || *c.Highlight > 255 {
			return fmt.Errorf("channel %d: the highlight level runs from 0 to 255, or is left empty", i+1)
		}
	}
	p.BuiltIn = false
	return nil
}

// Fixture is one patched fixture.
type Fixture struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Profile  string `json:"profile"`
	Universe int    `json:"universe"`
	Address  int    `json:"address"` // 1-512
}

// output is the controller's state: one per process.
type output struct {
	mu       sync.Mutex
	sender   *Sender
	loaded   bool
	profiles []Profile // the operator's own
	fixtures []Fixture
	nextID   int
	// Highlighted fixtures, by id: the levels their channels had before, to fade back to.
	lit map[int]map[int]byte
	// The fade a fixture is in, by id. A newer fade, Clear or Remove changes it and the old fade stops.
	fading  map[int]int
	fadeSeq int
	// Universes the operator ticked to send with nothing on them: kept, at zero,
	// where an idle universe is otherwise let go.
	keep map[int]bool
}

var out = &output{sender: NewSender()}

// Output is the sender the controller drives.
func Output() *Sender { return out.sender }

func beside(name string) string { return filepath.Join(filepath.Dir(settingsPath()), name) }

func writeFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// load reads the saved profiles and patch once. Call with mu held.
func (o *output) load() {
	if o.loaded {
		return
	}
	o.loaded = true
	o.nextID = 1
	if b, err := os.ReadFile(beside("sacn-profiles.json")); err == nil {
		var ps []Profile
		if err := json.Unmarshal(b, &ps); err != nil {
			luminex.Warnf("sACN: %s cannot be read: %v", beside("sacn-profiles.json"), err)
		}
		for _, p := range ps {
			if p.validate() == nil && o.profile(p.Name) == nil {
				o.profiles = append(o.profiles, p)
			}
		}
	}
	if b, err := os.ReadFile(beside("sacn-patch.json")); err == nil {
		var fs []Fixture
		if err := json.Unmarshal(b, &fs); err != nil {
			luminex.Warnf("sACN: %s cannot be read: %v", beside("sacn-patch.json"), err)
		}
		for _, f := range fs {
			if f.Universe < 1 || f.Universe > MaxUniverse || f.Address < 1 || f.Address > 512 {
				continue
			}
			if o.sender.Ensure(uint16(f.Universe)) != nil {
				continue
			}
			f.ID = o.nextID
			o.nextID++
			o.fixtures = append(o.fixtures, f)
			o.defaults(f)
		}
	}
	// Which universes are sent: the ones ticked with nothing on them, and the
	// ones with a patch that were unticked.
	o.keep = map[int]bool{}
	if b, err := os.ReadFile(beside("sacn-send.json")); err == nil {
		var sv sendFile
		if err := json.Unmarshal(b, &sv); err != nil {
			luminex.Warnf("sACN: %s cannot be read: %v", beside("sacn-send.json"), err)
		}
		for _, u := range sv.Keep {
			if u >= 1 && u <= MaxUniverse && o.sender.Ensure(uint16(u)) == nil {
				o.keep[u] = true
			}
		}
		for _, u := range sv.Off {
			if u >= 1 && u <= MaxUniverse {
				_ = o.sender.SetSending(uint16(u), false) // nothing happens unless it is carried
			}
		}
	}
}

// sendFile is sacn-send.json: the universes kept with nothing on them, and the
// carried ones that are not sent.
type sendFile struct {
	Keep []int `json:"keep"`
	Off  []int `json:"off"`
}

// setSend ticks or unticks universes for sending. Universe 0 is every universe
// the sender carries. Ticking one it does not carry adds it, at zero, and keeps
// it until it is let go. Call with mu held.
func (o *output) setSend(universe int, on bool) error {
	us := []int{universe}
	if universe == 0 {
		us = us[:0]
		for _, u := range o.sender.Universes() {
			us = append(us, int(u))
		}
	} else if universe < 1 || universe > MaxUniverse {
		return fmt.Errorf("universes run from 1 to %d", MaxUniverse)
	}
	for _, u := range us {
		if _, carried := o.sender.Levels(uint16(u)); !carried {
			if !on {
				continue
			}
			o.keep[u] = true
		}
		if err := o.sender.SetSending(uint16(u), on); err != nil {
			delete(o.keep, u)
			return err
		}
	}
	if universe == 0 {
		luminex.Logf("sACN output: every universe %s", map[bool]string{true: "ticked to send", false: "unticked, none is sent"}[on])
	} else {
		luminex.Logf("sACN output: universe %d %s", universe, map[bool]string{true: "ticked to send", false: "unticked, not sent"}[on])
	}
	o.save("send")
	return nil
}

func (o *output) save(what string) {
	var err error
	if what == "profiles" {
		err = writeFile(beside("sacn-profiles.json"), o.profiles)
	} else if what == "send" {
		sv := sendFile{Keep: []int{}, Off: []int{}}
		for _, u := range o.sender.Universes() {
			if o.keep[int(u)] {
				sv.Keep = append(sv.Keep, int(u))
			}
			if !o.sender.Sending(u) {
				sv.Off = append(sv.Off, int(u))
			}
		}
		err = writeFile(beside("sacn-send.json"), sv)
	} else {
		err = writeFile(beside("sacn-patch.json"), o.fixtures)
		defer o.save("send") // a patch change can let a universe go
	}
	if err != nil {
		luminex.Warnf("sACN: could not save the %s: %v", what, err)
	}
}

// profile finds a profile by name, ignoring case: the operator's own, then the built-in ones.
func (o *output) profile(name string) *Profile {
	for i := range o.profiles {
		if strings.EqualFold(o.profiles[i].Name, name) {
			return &o.profiles[i]
		}
	}
	for i := range builtIn {
		if strings.EqualFold(builtIn[i].Name, name) {
			return &builtIn[i]
		}
	}
	return nil
}

func (o *output) allProfiles() []Profile {
	all := append(append([]Profile{}, builtIn...), o.profiles...)
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].BuiltIn != all[j].BuiltIn {
			return all[i].BuiltIn
		}
		return strings.ToLower(all[i].Name) < strings.ToLower(all[j].Name)
	})
	return all
}

// defaults puts a fixture's channels at their profile's default levels.
func (o *output) defaults(f Fixture) {
	p := o.profile(f.Profile)
	if p == nil {
		return
	}
	set := map[int]byte{}
	for i, c := range p.Channels {
		if f.Address+i <= 512 {
			set[f.Address+i] = byte(c.Default)
		}
	}
	_ = o.sender.Set(uint16(f.Universe), set)
}

func (o *output) footprint(f Fixture) int {
	if p := o.profile(f.Profile); p != nil {
		return len(p.Channels)
	}
	return 1
}

// highlight fades a fixture to its profile's highlight levels, or back to the
// levels it had before. Call with mu held; the fade runs on after it returns.
func (o *output) highlight(id int, on bool) error {
	var f *Fixture
	for i := range o.fixtures {
		if o.fixtures[i].ID == id {
			f = &o.fixtures[i]
		}
	}
	if f == nil {
		return fmt.Errorf("that fixture is no longer patched")
	}
	if o.lit == nil {
		o.lit, o.fading = map[int]map[int]byte{}, map[int]int{}
	}
	now, _ := o.sender.Levels(uint16(f.Universe))
	from, to := map[int]byte{}, map[int]byte{}
	if on {
		if o.lit[id] != nil {
			return nil
		}
		if p := o.profile(f.Profile); p != nil {
			for i, c := range p.Channels {
				if a := f.Address + i; c.Highlight != nil && *c.Highlight >= 0 && a <= 512 {
					from[a], to[a] = now[a-1], byte(*c.Highlight)
				}
			}
		}
		if len(to) == 0 {
			return fmt.Errorf("the profile %q has no highlight levels: set them on Profiles", f.Profile)
		}
		o.lit[id] = from
	} else {
		to = o.lit[id]
		if to == nil {
			return nil
		}
		for a := range to {
			from[a] = now[a-1]
		}
		delete(o.lit, id)
	}
	o.fadeSeq++
	seq := o.fadeSeq
	o.fading[id] = seq
	go o.fade(id, seq, uint16(f.Universe), from, to)
	return nil
}

func (o *output) fade(id, seq int, u uint16, from, to map[int]byte) {
	const steps = 20
	for s := 1; s <= steps; s++ {
		time.Sleep(HighlightFade / steps)
		o.mu.Lock()
		if o.fading[id] != seq {
			o.mu.Unlock()
			return
		}
		set := map[int]byte{}
		for a, t := range to {
			set[a] = byte(int(from[a]) + (int(t)-int(from[a]))*s/steps)
		}
		_ = o.sender.Set(u, set)
		o.mu.Unlock()
	}
}

// clash names a patched fixture that shares a channel with the range, if any.
func (o *output) clash(universe, address, footprint, exceptID int) string {
	for _, f := range o.fixtures {
		if f.ID != exceptID && f.Universe == universe && address < f.Address+o.footprint(f) && f.Address < address+footprint {
			return f.Name
		}
	}
	return ""
}

var trailingNumber = regexp.MustCompile(`^(.*?)(\d+)$`)

// add patches count fixtures in a row from the address, numbering their names.
func (o *output) add(name, profile string, universe, address, count int) error {
	p := o.profile(profile)
	name = strings.TrimSpace(name)
	switch {
	case p == nil:
		return fmt.Errorf("there is no profile called %q", profile)
	case universe < 1 || universe > MaxUniverse:
		return fmt.Errorf("universes run from 1 to %d", MaxUniverse)
	case address < 1 || address > 512:
		return fmt.Errorf("addresses run from 1 to 512")
	case count < 1 || count > 512:
		return fmt.Errorf("add between 1 and 512 fixtures at a time")
	case len(name) > 60:
		return fmt.Errorf("the name is longer than 60 characters")
	}
	n := len(p.Channels)
	if last := address + count*n - 1; last > 512 {
		return fmt.Errorf("%d × %s (%d channels each) from address %d would run to %d, past 512", count, p.Name, n, address, last)
	}
	if other := o.clash(universe, address, count*n, 0); other != "" {
		return fmt.Errorf("universe %d, channels %d-%d: %q is already patched there", universe, address, address+count*n-1, other)
	}
	if err := o.sender.Ensure(uint16(universe)); err != nil {
		return err
	}
	if name == "" {
		name = p.Name + " 1"
	}
	stem, first, numbered := name, 1, false
	if m := trailingNumber.FindStringSubmatch(name); m != nil {
		stem, numbered = m[1], true
		first, _ = strconv.Atoi(m[2])
	}
	for i := 0; i < count; i++ {
		f := Fixture{ID: o.nextID, Name: name, Profile: p.Name, Universe: universe, Address: address + i*n}
		if numbered {
			f.Name = stem + strconv.Itoa(first+i)
		} else if count > 1 {
			f.Name = name + " " + strconv.Itoa(i+1)
		}
		o.nextID++
		o.fixtures = append(o.fixtures, f)
		o.defaults(f)
	}
	sort.SliceStable(o.fixtures, func(i, j int) bool {
		a, b := o.fixtures[i], o.fixtures[j]
		return a.Universe < b.Universe || a.Universe == b.Universe && a.Address < b.Address
	})
	o.save("patch")
	luminex.Logf("sACN output: patched %d × %s at universe %d, address %d", count, p.Name, universe, address)
	return nil
}

// remove takes fixtures off the patch, puts their channels at zero and lets go of
// a universe that has no fixture and no level left.
func (o *output) remove(ids []int) {
	gone := map[int]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	keep := o.fixtures[:0:0]
	touched := map[int]bool{}
	for _, f := range o.fixtures {
		if !gone[f.ID] {
			keep = append(keep, f)
			continue
		}
		delete(o.lit, f.ID)
		delete(o.fading, f.ID)
		set := map[int]byte{}
		for i := 0; i < o.footprint(f) && f.Address+i <= 512; i++ {
			set[f.Address+i] = 0
		}
		_ = o.sender.Set(uint16(f.Universe), set)
		touched[f.Universe] = true
		luminex.Logf("sACN output: removed %q (universe %d, address %d)", f.Name, f.Universe, f.Address)
	}
	o.fixtures = keep
	for u := range touched {
		o.releaseIfIdle(u)
	}
	o.save("patch")
}

func (o *output) releaseIfIdle(u int) {
	if o.keep[u] {
		return
	}
	for _, f := range o.fixtures {
		if f.Universe == u {
			return
		}
	}
	if l, ok := o.sender.Levels(uint16(u)); ok && l == [512]byte{} {
		o.sender.Release(uint16(u))
	}
}

// clear puts every fixture at its defaults and every other channel at zero.
func (o *output) clear() {
	o.lit, o.fading = map[int]map[int]byte{}, map[int]int{} // highlights end here too
	for _, u := range o.sender.Universes() {
		set := map[int]byte{}
		for a := 1; a <= 512; a++ {
			set[a] = 0
		}
		_ = o.sender.Set(u, set)
	}
	for _, f := range o.fixtures {
		o.defaults(f)
	}
	for _, u := range o.sender.Universes() {
		o.releaseIfIdle(int(u))
	}
	luminex.Logf("sACN output: cleared (fixtures at their defaults, everything else at zero)")
}

type fixtureView struct {
	Fixture
	Channels     []Channel `json:"channels"`
	Problem      string    `json:"problem,omitempty"`
	Highlight    bool      `json:"highlight"`    // highlighted now
	CanHighlight bool      `json:"canHighlight"` // its profile has highlight levels
}

type otherSource struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Priority int    `json:"priority"`
}

type outUniverse struct {
	Universe int           `json:"universe"`
	Levels   string        `json:"levels"` // base64 of 512 octets
	Fixtures int           `json:"fixtures"`
	Sending  bool          `json:"sending"` // ticked: sent while the output is on
	Others   []otherSource `json:"others"`  // other sources heard on this universe right now
}

type outState struct {
	SenderState
	Dry        bool              `json:"dry"`
	Interfaces []OutputInterface `json:"interfaces"`
	Universes  []outUniverse     `json:"universes"`
	Fixtures   []fixtureView     `json:"fixtures"`
	Profiles   []Profile         `json:"profiles"`
	Types      []string          `json:"types"`
	MaxOutput  int               `json:"maxOutput"`
	Folder     string            `json:"folder"`
}

func (o *output) state() outState {
	// Ask the receiver first: it asks the sender for its universes, so neither lock is held across the other.
	heard := map[int][]otherSource{}
	if rx, err := Shared(); err == nil {
		own := o.sender.CID().String()
		for _, u := range rx.Status().Universes {
			for _, s := range u.Sources {
				if s.Live && !s.Preview && s.CID != own {
					heard[u.Universe] = append(heard[u.Universe], otherSource{Name: s.Name, IP: s.IP, Priority: s.Priority})
				}
			}
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.load()
	st := outState{
		SenderState: o.sender.State(), Dry: luminex.DryRun, Interfaces: OutputInterfaces(),
		Universes: []outUniverse{}, Fixtures: []fixtureView{}, Profiles: o.allProfiles(), Types: ChannelTypes,
		MaxOutput: MaxOutput, Folder: filepath.Dir(settingsPath()),
	}
	if st.Interface == "" {
		s := loadSettings()
		st.Interface = s.Interface
		if s.Priority != nil {
			st.Priority = *s.Priority
		}
		if s.Name != "" {
			st.Name = s.Name
		}
	}
	count := map[int]int{}
	for _, f := range o.fixtures {
		v := fixtureView{Fixture: f, Channels: []Channel{}, Highlight: o.lit[f.ID] != nil}
		if p := o.profile(f.Profile); p == nil {
			v.Problem = fmt.Sprintf("The profile %q is gone. Remove this fixture, or save a profile with that name.", f.Profile)
		} else {
			v.Channels = p.Channels
			for _, c := range p.Channels {
				v.CanHighlight = v.CanHighlight || c.Highlight != nil && *c.Highlight >= 0
			}
			if f.Address+len(p.Channels)-1 > 512 {
				v.Problem = "Runs past channel 512 with this profile."
			} else if other := o.clash(f.Universe, f.Address, len(p.Channels), f.ID); other != "" {
				v.Problem = fmt.Sprintf("Shares channels with %q since its profile changed.", other)
			}
		}
		count[f.Universe]++
		st.Fixtures = append(st.Fixtures, v)
	}
	for _, u := range o.sender.Universes() {
		l, _ := o.sender.Levels(u)
		others := heard[int(u)]
		if others == nil {
			others = []otherSource{}
		}
		st.Universes = append(st.Universes, outUniverse{Universe: int(u), Levels: base64.StdEncoding.EncodeToString(l[:]), Fixtures: count[int(u)], Sending: o.sender.Sending(u), Others: others})
	}
	return st
}

func readBody(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// mountOutput registers the controller's API.
func mountOutput(mux *http.ServeMux) {
	o := out
	fail := func(w http.ResponseWriter, err error) { writeJSON(w, 400, map[string]string{"error": err.Error()}) }
	// do runs a change with the state locked and answers with the new state.
	do := func(w http.ResponseWriter, change func() error) {
		o.mu.Lock()
		o.load()
		err := change()
		o.mu.Unlock()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, o.state())
	}
	mux.HandleFunc("GET /api/sacn/out", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, o.state()) })
	mux.HandleFunc("POST /api/sacn/out/start", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Interface string `json:"interface"`
			Priority  *int   `json:"priority"`
			Name      string `json:"name"`
		}
		if err := readBody(r, &body); err != nil {
			fail(w, err)
			return
		}
		do(w, func() error {
			if body.Priority == nil {
				p := DefaultPriority
				body.Priority = &p
			}
			if len(o.sender.Universes()) == 0 {
				return fmt.Errorf("nothing to send yet: patch a fixture or set a channel first")
			}
			if err := o.sender.Start(body.Interface, *body.Priority, strings.TrimSpace(body.Name)); err != nil {
				return err
			}
			s := loadSettings()
			s.Interface, s.Priority, s.Name = body.Interface, body.Priority, strings.TrimSpace(body.Name)
			if err := saveSettings(s); err != nil {
				luminex.Warnf("sACN: could not save the output settings: %v", err)
			}
			return nil
		})
	})
	mux.HandleFunc("POST /api/sacn/out/stop", func(w http.ResponseWriter, r *http.Request) {
		o.sender.Stop()
		writeJSON(w, 200, o.state())
	})
	mux.HandleFunc("POST /api/sacn/out/levels", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Universe int      `json:"universe"`
			Set      [][2]int `json:"set"` // [address, level] pairs
		}
		if err := readBody(r, &body); err != nil {
			fail(w, err)
			return
		}
		set := map[int]byte{}
		for _, p := range body.Set {
			if p[1] < 0 || p[1] > 255 {
				fail(w, fmt.Errorf("levels run from 0 to 255"))
				return
			}
			set[p[0]] = byte(p[1])
		}
		if body.Universe < 1 || body.Universe > MaxUniverse {
			fail(w, fmt.Errorf("universes run from 1 to %d", MaxUniverse))
			return
		}
		if err := o.sender.Set(uint16(body.Universe), set); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/sacn/out/clear", func(w http.ResponseWriter, r *http.Request) {
		do(w, func() error { o.clear(); return nil })
	})
	mux.HandleFunc("POST /api/sacn/out/release", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Universe int `json:"universe"`
		}
		if err := readBody(r, &body); err != nil {
			fail(w, err)
			return
		}
		do(w, func() error {
			for _, f := range o.fixtures {
				if f.Universe == body.Universe {
					return fmt.Errorf("universe %d has fixtures patched: remove them first", body.Universe)
				}
			}
			o.sender.Release(uint16(body.Universe))
			delete(o.keep, body.Universe)
			o.save("send")
			luminex.Logf("sACN output: let go of universe %d", body.Universe)
			return nil
		})
	})
	// Tick or untick a universe for sending; universe 0 is every one carried.
	mux.HandleFunc("POST /api/sacn/out/send", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Universe int  `json:"universe"`
			On       bool `json:"on"`
		}
		if err := readBody(r, &body); err != nil {
			fail(w, err)
			return
		}
		do(w, func() error { return o.setSend(body.Universe, body.On) })
	})
	mux.HandleFunc("POST /api/sacn/out/fixtures/add", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name     string `json:"name"`
			Profile  string `json:"profile"`
			Universe int    `json:"universe"`
			Address  int    `json:"address"`
			Count    int    `json:"count"`
		}
		if err := readBody(r, &body); err != nil {
			fail(w, err)
			return
		}
		if body.Count == 0 {
			body.Count = 1
		}
		do(w, func() error { return o.add(body.Name, body.Profile, body.Universe, body.Address, body.Count) })
	})
	mux.HandleFunc("POST /api/sacn/out/fixtures/remove", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []int `json:"ids"`
		}
		if err := readBody(r, &body); err != nil {
			fail(w, err)
			return
		}
		do(w, func() error { o.remove(body.IDs); return nil })
	})
	mux.HandleFunc("POST /api/sacn/out/fixtures/highlight", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID int  `json:"id"`
			On bool `json:"on"`
		}
		if err := readBody(r, &body); err != nil {
			fail(w, err)
			return
		}
		do(w, func() error { return o.highlight(body.ID, body.On) })
	})
	mux.HandleFunc("POST /api/sacn/out/profiles/save", func(w http.ResponseWriter, r *http.Request) {
		var p Profile
		if err := readBody(r, &p); err != nil {
			fail(w, fmt.Errorf("that is not a profile: %v", err))
			return
		}
		do(w, func() error {
			if err := p.validate(); err != nil {
				return err
			}
			for _, b := range builtIn {
				if strings.EqualFold(b.Name, p.Name) {
					return fmt.Errorf("%q is a built-in profile: save yours under another name", b.Name)
				}
			}
			replaced := false
			for i := range o.profiles {
				if strings.EqualFold(o.profiles[i].Name, p.Name) {
					o.profiles[i], replaced = p, true
				}
			}
			if !replaced {
				o.profiles = append(o.profiles, p)
			}
			o.save("profiles")
			luminex.Logf("sACN output: saved profile %q (%d channels)", p.Name, len(p.Channels))
			return nil
		})
	})
	mux.HandleFunc("POST /api/sacn/out/profiles/delete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if err := readBody(r, &body); err != nil {
			fail(w, err)
			return
		}
		do(w, func() error {
			for i := range o.profiles {
				if strings.EqualFold(o.profiles[i].Name, body.Name) {
					for _, f := range o.fixtures {
						if strings.EqualFold(f.Profile, body.Name) {
							return fmt.Errorf("%q uses this profile: remove the fixtures first", f.Name)
						}
					}
					o.profiles = append(o.profiles[:i], o.profiles[i+1:]...)
					o.save("profiles")
					return nil
				}
			}
			return fmt.Errorf("there is no saved profile called %q", body.Name)
		})
	})
}
