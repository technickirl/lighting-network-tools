package sacn

// The receiver: one UDP socket on port 5568 that joins the multicast group of
// every universe being watched, on every network interface, and remembers what
// each source is sending. It only listens. Joining a group makes this computer
// send an IGMP report, which is how a switch knows to pass the universe here;
// nothing else goes out.

import (
	"encoding/base64"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

const (
	// MaxWatched is how many universes are joined at once, typed and discovered together.
	MaxWatched = 128

	liveFor    = 2500 * time.Millisecond // E1.31's network data loss timeout
	forgetFor  = 30 * time.Second        // a source that stopped stays listed this long
	discoverOK = 30 * time.Second        // a source repeats its universe list every 10 s
)

type source struct {
	cid        CID
	name, ip   string
	priority   uint8
	sync       uint16
	seq        uint8
	seqKnown   bool
	preview    bool
	terminated bool
	pap        bool // sends per-address priority (start code 0xDD)
	levels     [512]byte
	slots      int
	packets    int
	lost       int
	outOfOrder int
	first      time.Time
	last       time.Time
	winStart   time.Time
	winCount   int
	fps        float64
}

type discovered struct {
	name, ip string
	pages    map[uint8][]uint16
	last     time.Time
}

// Receiver holds the socket and everything heard on it.
type Receiver struct {
	mu      sync.Mutex
	conn    *net.UDPConn
	pc      *ipv4.PacketConn
	manual  []uint16
	also    func() []uint16         // more universes to join: the ones this tool sends
	joined  map[uint16]map[int]bool // universe → interface index
	joinErr map[uint16]string
	unis    map[uint16]map[CID]*source
	disc    map[CID]*discovered
	ifaces  []string
	stop    chan struct{}
	now     func() time.Time
}

func newReceiver() *Receiver {
	return &Receiver{
		joined: map[uint16]map[int]bool{}, joinErr: map[uint16]string{},
		unis: map[uint16]map[CID]*source{}, disc: map[CID]*discovered{},
		stop: make(chan struct{}), now: time.Now,
	}
}

// Open starts listening on the port (Port, or 0 in a test for any free one).
// The socket is opened so that other sACN software on this computer can hold the
// same port at the same time.
func Open(port int) (*Receiver, error) {
	conn, err := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: GroupAddr(DiscoveryUniverse), Port: port})
	if err != nil {
		return nil, fmt.Errorf("cannot listen on UDP %d: %w", port, err)
	}
	_ = conn.SetReadBuffer(1 << 20)
	r := newReceiver()
	r.conn, r.pc = conn, ipv4.NewPacketConn(conn)
	r.mu.Lock()
	r.reconcile()
	r.mu.Unlock()
	go r.read()
	go r.keepJoined()
	return r, nil
}

// Port is the UDP port the receiver is on.
func (r *Receiver) Port() int {
	if r.conn == nil {
		return 0
	}
	return r.conn.LocalAddr().(*net.UDPAddr).Port
}

// Close stops listening and leaves every group.
func (r *Receiver) Close() {
	close(r.stop)
	if r.conn != nil {
		_ = r.conn.Close()
	}
}

func (r *Receiver) read() {
	buf := make([]byte, 2048)
	for {
		n, from, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		r.handle(buf[:n], from.IP)
	}
}

// keepJoined joins again when a network interface appears and when a source
// lists a universe it sends.
func (r *Receiver) keepJoined() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.mu.Lock()
			r.reconcile()
			r.mu.Unlock()
		}
	}
}

// multicastInterfaces: every interface that is up, can do multicast and has an IPv4 address.
func multicastInterfaces() ([]net.Interface, []string) {
	var out []net.Interface
	var names []string
	ifis, _ := net.Interfaces()
	for _, ifi := range ifis {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				out = append(out, ifi)
				names = append(names, fmt.Sprintf("%s (%s)", ifi.Name, ipn.IP.To4()))
				break
			}
		}
	}
	return out, names
}

func alreadyJoined(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "already in use") || strings.Contains(s, "only one usage")
}

// wanted is the universes to join: the typed ones first, then those that sources
// say they send, up to MaxWatched. The discovery universe is always joined.
func (r *Receiver) wanted() map[uint16]bool {
	want := map[uint16]bool{}
	for _, u := range r.manual {
		if len(want) < MaxWatched {
			want[u] = true
		}
	}
	if r.also != nil {
		for _, u := range r.also() {
			want[u] = true
		}
	}
	now := r.now()
	var found []uint16
	for _, d := range r.disc {
		if now.Sub(d.last) > discoverOK {
			continue
		}
		for _, us := range d.pages {
			found = append(found, us...)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i] < found[j] })
	for _, u := range found {
		if u >= 1 && u <= MaxUniverse && len(want) < MaxWatched {
			want[u] = true
		}
	}
	return want
}

// reconcile makes the groups joined match the universes wanted. Call with mu held.
func (r *Receiver) reconcile() {
	if r.pc == nil {
		return
	}
	ifis, names := multicastInterfaces()
	r.ifaces = names
	present := map[int]bool{}
	for _, ifi := range ifis {
		present[ifi.Index] = true
	}
	want := r.wanted()
	want[DiscoveryUniverse] = true
	for u, on := range r.joined {
		for idx := range on {
			if !present[idx] {
				delete(on, idx) // the interface went away; join again when it is back
			}
		}
		if want[u] {
			continue
		}
		for i := range ifis {
			if on[ifis[i].Index] {
				_ = r.pc.LeaveGroup(&ifis[i], &net.UDPAddr{IP: GroupAddr(u)})
			}
		}
		delete(r.joined, u)
		delete(r.joinErr, u)
	}
	for u := range want {
		if r.joined[u] == nil {
			r.joined[u] = map[int]bool{}
		}
		delete(r.joinErr, u)
		for i := range ifis {
			if r.joined[u][ifis[i].Index] {
				continue
			}
			err := r.pc.JoinGroup(&ifis[i], &net.UDPAddr{IP: GroupAddr(u)})
			if err != nil && !alreadyJoined(err) {
				r.joinErr[u] = fmt.Sprintf("could not join on %s: %v", ifis[i].Name, err)
				continue
			}
			r.joined[u][ifis[i].Index] = true
		}
	}
}

// SetWatch replaces the typed list of universes to listen to.
func (r *Receiver) SetWatch(us []uint16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.manual = append([]uint16(nil), us...)
	r.reconcile()
}

// handle takes one packet off the network.
func (r *Receiver) handle(b []byte, from net.IP) {
	p, err := Parse(b)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	switch p := p.(type) {
	case *Discovery:
		d := r.disc[p.CID]
		if d == nil {
			d = &discovered{pages: map[uint8][]uint16{}}
			r.disc[p.CID] = d
		}
		if p.Page == 0 {
			d.pages = map[uint8][]uint16{} // a new list starts at page 0
		}
		d.name, d.ip, d.last = p.Source, from.String(), now
		d.pages[p.Page] = p.Universes
	case *Data:
		if p.Universe < 1 || p.Universe > MaxUniverse {
			return
		}
		srcs := r.unis[p.Universe]
		if srcs == nil {
			srcs = map[CID]*source{}
			r.unis[p.Universe] = srcs
		}
		s := srcs[p.CID]
		if s == nil || now.Sub(s.last) > liveFor {
			// New, or back after a gap: its sequence numbers start again.
			keep := s
			s = &source{cid: p.CID, first: now, winStart: now}
			if keep != nil {
				s.lost, s.outOfOrder, s.packets, s.first = keep.lost, keep.outOfOrder, keep.packets, keep.first
				s.levels, s.slots, s.pap = keep.levels, keep.slots, keep.pap
			}
			srcs[p.CID] = s
		}
		if s.seqKnown {
			// E1.31 6.7.2: a packet up to 20 behind the last one is late and is dropped.
			if diff := int8(p.Seq - s.seq); diff <= 0 && diff > -20 {
				s.outOfOrder++
				return
			} else if diff > 1 {
				s.lost += int(diff) - 1
			}
		}
		s.seq, s.seqKnown = p.Seq, true
		s.name, s.ip, s.priority, s.sync, s.last = p.Source, from.String(), p.Priority, p.Sync, now
		s.preview = p.Options&OptPreview != 0
		s.terminated = p.Options&OptTerminated != 0
		s.packets++
		switch p.StartCode {
		case 0:
			s.levels = [512]byte{}
			copy(s.levels[:], p.Slots)
			s.slots = len(p.Slots)
			s.winCount++
			if el := now.Sub(s.winStart); el >= time.Second {
				s.fps = float64(s.winCount) / el.Seconds()
				s.winStart, s.winCount = now, 0
			}
		case 0xDD:
			s.pap = true
		}
	}
}

// SourceInfo is one source on one universe, as the page shows it.
type SourceInfo struct {
	CID                string  `json:"cid"`
	Name               string  `json:"name"`
	IP                 string  `json:"ip"`
	Priority           int     `json:"priority"`
	FPS                float64 `json:"fps"`
	Slots              int     `json:"slots"`
	Live               bool    `json:"live"`
	AgeMs              int64   `json:"ageMs"`
	Preview            bool    `json:"preview"`
	Terminated         bool    `json:"terminated"`
	PerAddressPriority bool    `json:"perAddressPriority"`
	Sync               int     `json:"sync"`
	Lost               int     `json:"lost"`
	OutOfOrder         int     `json:"outOfOrder"`
	Winning            bool    `json:"winning"`          // its levels are in the merged result
	Levels             string  `json:"levels,omitempty"` // base64 of 512 octets
}

// UniverseInfo is one universe: who sends it and, when asked for, the levels.
type UniverseInfo struct {
	Universe  int          `json:"universe"`
	Watched   bool         `json:"watched"`
	Typed     bool         `json:"typed"` // on the typed list, not only found by discovery
	JoinError string       `json:"joinError,omitempty"`
	Sources   []SourceInfo `json:"sources"`
	Merged    string       `json:"merged,omitempty"` // base64 of 512 octets: highest priority wins, highest level among equals
}

// DiscoveredInfo is a source that announces the universes it sends.
type DiscoveredInfo struct {
	CID       string `json:"cid"`
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Universes string `json:"universes"`
}

// Status is everything but the levels.
type Status struct {
	Listening  bool             `json:"listening"`
	Error      string           `json:"error,omitempty"`
	Port       int              `json:"port"`
	Watch      string           `json:"watch"`
	Max        int              `json:"max"`
	Interfaces []string         `json:"interfaces"`
	Universes  []UniverseInfo   `json:"universes"`
	Discovered []DiscoveredInfo `json:"discovered"`
}

// universe builds one universe's view and drops sources gone for too long. Call with mu held.
func (r *Receiver) universe(u uint16, want map[uint16]bool, levels bool) UniverseInfo {
	now := r.now()
	info := UniverseInfo{Universe: int(u), Watched: want[u], JoinError: r.joinErr[u], Sources: []SourceInfo{}}
	for _, m := range r.manual {
		info.Typed = info.Typed || m == u
	}
	srcs := r.unis[u]
	var live []*source
	top := -1
	for cid, s := range srcs {
		age := now.Sub(s.last)
		if age > forgetFor {
			delete(srcs, cid)
			continue
		}
		if age <= liveFor && !s.terminated && !s.preview {
			live = append(live, s)
			top = max(top, int(s.priority))
		}
	}
	if len(srcs) == 0 {
		delete(r.unis, u)
	}
	var merged [512]byte
	list := make([]*source, 0, len(srcs))
	for _, s := range srcs {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].priority != list[j].priority {
			return list[i].priority > list[j].priority
		}
		return list[i].name+list[i].cid.String() < list[j].name+list[j].cid.String()
	})
	for _, s := range list {
		age := now.Sub(s.last)
		si := SourceInfo{
			CID: s.cid.String(), Name: s.name, IP: s.ip, Priority: int(s.priority), Slots: s.slots,
			Live: age <= liveFor && !s.terminated, AgeMs: age.Milliseconds(), Preview: s.preview, Terminated: s.terminated,
			PerAddressPriority: s.pap, Sync: int(s.sync), Lost: s.lost, OutOfOrder: s.outOfOrder,
		}
		if si.Live {
			si.FPS = float64(int(s.fps*10+0.5)) / 10
		}
		if si.Live && !s.preview && int(s.priority) == top {
			si.Winning = true
			for i, v := range s.levels {
				merged[i] = max(merged[i], v)
			}
		}
		if levels {
			si.Levels = base64.StdEncoding.EncodeToString(s.levels[:])
		}
		info.Sources = append(info.Sources, si)
	}
	if levels {
		info.Merged = base64.StdEncoding.EncodeToString(merged[:])
	}
	return info
}

// Universe is one universe with its levels.
func (r *Receiver) Universe(u uint16) UniverseInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.universe(u, r.wanted(), true)
}

// Status lists every universe watched or heard, without levels.
func (r *Receiver) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	want := r.wanted()
	st := Status{
		Listening: r.conn != nil, Port: r.Port(), Watch: FormatUniverses(r.manual), Max: MaxWatched,
		Interfaces: append([]string{}, r.ifaces...), Universes: []UniverseInfo{}, Discovered: []DiscoveredInfo{},
	}
	all := map[uint16]bool{}
	for u := range want {
		all[u] = true
	}
	for u := range r.unis {
		all[u] = true
	}
	us := make([]uint16, 0, len(all))
	for u := range all {
		us = append(us, u)
	}
	sort.Slice(us, func(i, j int) bool { return us[i] < us[j] })
	for _, u := range us {
		info := r.universe(u, want, false)
		if info.Watched || len(info.Sources) > 0 {
			st.Universes = append(st.Universes, info)
		}
	}
	for cid, d := range r.disc {
		if now.Sub(d.last) > discoverOK {
			delete(r.disc, cid)
			continue
		}
		var list []uint16
		for _, p := range d.pages {
			list = append(list, p...)
		}
		st.Discovered = append(st.Discovered, DiscoveredInfo{CID: cid.String(), Name: d.name, IP: d.ip, Universes: FormatUniverses(list)})
	}
	sort.Slice(st.Discovered, func(i, j int) bool {
		return st.Discovered[i].Name+st.Discovered[i].CID < st.Discovered[j].Name+st.Discovered[j].CID
	})
	return st
}

// ParseUniverses reads a list such as "1-8, 33": numbers and ranges, split by
// commas or spaces, each from 1 to 63999.
func ParseUniverses(s string) ([]uint16, error) {
	seen := map[uint16]bool{}
	for _, part := range strings.FieldsFunc(s, func(c rune) bool { return c == ',' || c == ' ' || c == ';' }) {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		b := a
		if err == nil && isRange {
			b, err = strconv.Atoi(hi)
		}
		if err != nil {
			return nil, fmt.Errorf("%q is not a universe or a range like 1-8", part)
		}
		if a < 1 || b > MaxUniverse || a > b {
			return nil, fmt.Errorf("%q: universes run from 1 to %d", part, MaxUniverse)
		}
		if len(seen)+b-a+1 > MaxWatched {
			return nil, fmt.Errorf("that is more than %d universes at once", MaxWatched)
		}
		for u := a; u <= b; u++ {
			seen[uint16(u)] = true
		}
	}
	out := make([]uint16, 0, len(seen))
	for u := range seen {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// FormatUniverses writes a list the way ParseUniverses reads it: "1-8, 33".
func FormatUniverses(us []uint16) string {
	us = append([]uint16(nil), us...)
	sort.Slice(us, func(i, j int) bool { return us[i] < us[j] })
	var parts []string
	for i := 0; i < len(us); {
		j := i
		for j+1 < len(us) && us[j+1] <= us[j]+1 {
			j++
		}
		if us[j] > us[i] {
			parts = append(parts, fmt.Sprintf("%d-%d", us[i], us[j]))
		} else {
			parts = append(parts, strconv.Itoa(int(us[i])))
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}
