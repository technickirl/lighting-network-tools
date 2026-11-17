package nodeconfig

// The RDM tab's state lives here, in the app, per node: the DMX ports as last
// read, each port's fixtures as last read (and every change made since), and
// which ports a read is running on. Every page with the node open gets it
// over /api/live as {type:"rdm", ip, rdm:{...}} whenever it changes, so a read
// or an edit on the laptop shows on the iPad at once, and a page opened later
// starts from it (GET /api/node/{ip}/rdm carries it as "state"). What each
// page is doing (the keypad, ticked tiles, the fixture page, Follow ID) stays
// in that page.

import (
	"sync"
	"time"
)

// rdmRead is one port's fixtures as last read.
type rdmRead struct {
	Port     int          `json:"port"`
	Universe int          `json:"universe"`
	Source   string       `json:"source"` // "artnet" or "overheard"
	Note     string       `json:"note,omitempty"`
	At       time.Time    `json:"at"`
	Fixtures []rdmFixture `json:"fixtures"`
}

type rdmNodeState struct {
	Rev   int              `json:"rev"` // goes up with every change; a page ignores an older one
	Ports []rdmPort        `json:"ports,omitempty"`
	Read  map[int]*rdmRead `json:"read"`
	Busy  map[int]string   `json:"busy"` // port → "Reading…" while a read runs
}

var rdmStore = struct {
	sync.Mutex
	m map[string]*rdmNodeState
}{m: map[string]*rdmNodeState{}}

// rdmChange changes a node's state under the lock and pushes the result to
// every page that has the node open.
func rdmChange(ip string, fn func(s *rdmNodeState)) {
	rdmStore.Lock()
	s := rdmStore.m[ip]
	if s == nil {
		s = &rdmNodeState{Read: map[int]*rdmRead{}, Busy: map[int]string{}}
		rdmStore.m[ip] = s
	}
	fn(s)
	s.Rev++
	snap := s.copy()
	rdmStore.Unlock()
	pushTo(ip, obj{"type": "rdm", "ip": ip, "rdm": snap})
}

// copy is deep enough that the snapshot can be encoded after the lock is released.
func (s *rdmNodeState) copy() *rdmNodeState {
	c := &rdmNodeState{Rev: s.Rev, Ports: append([]rdmPort(nil), s.Ports...), Read: map[int]*rdmRead{}, Busy: map[int]string{}}
	for p, r := range s.Read {
		rc := *r
		rc.Fixtures = make([]rdmFixture, len(r.Fixtures)) // never nil: a port with no fixtures is [], not null
		copy(rc.Fixtures, r.Fixtures)
		c.Read[p] = &rc
	}
	for p, b := range s.Busy {
		c.Busy[p] = b
	}
	return c
}

// rdmSnapshot is a node's state now (empty when nothing has been read).
func rdmSnapshot(ip string) *rdmNodeState {
	rdmStore.Lock()
	defer rdmStore.Unlock()
	if s := rdmStore.m[ip]; s != nil {
		return s.copy()
	}
	return &rdmNodeState{Read: map[int]*rdmRead{}, Busy: map[int]string{}}
}

// rdmStorePorts records the DMX ports as just read; a port with RDM off loses its fixtures.
func rdmStorePorts(ip string, ports []rdmPort) {
	rdmChange(ip, func(s *rdmNodeState) {
		s.Ports = ports
		for _, p := range ports {
			if !p.RDM {
				delete(s.Read, p.Port)
			}
		}
	})
}

// rdmStoreCount records how many fixtures the node lists on a port, as just
// asked (while the page waits on a search, and at every read), so no page goes
// on showing the count from when the ports were read. Pushed only on a change.
func rdmStoreCount(ip string, port, n int) {
	rdmStore.Lock()
	s, at := rdmStore.m[ip], -1
	if s != nil {
		for i, p := range s.Ports {
			if p.Port == port && p.Count != n {
				at = i
			}
		}
	}
	rdmStore.Unlock()
	if at < 0 {
		return
	}
	rdmChange(ip, func(s *rdmNodeState) {
		ports := append([]rdmPort(nil), s.Ports...) // a snapshot already sent keeps its own copy
		for i := range ports {
			if ports[i].Port == port {
				ports[i].Count = n
			}
		}
		s.Ports = ports
	})
}

func rdmStoreBusy(ip string, port int, what string) {
	rdmChange(ip, func(s *rdmNodeState) {
		if what == "" {
			delete(s.Busy, port)
		} else {
			s.Busy[port] = what
		}
	})
}

func rdmStoreRead(ip string, r *rdmRead) {
	rdmChange(ip, func(s *rdmNodeState) { s.Read[r.Port] = r })
}

// rdmStoreFixture puts a fixture as just written or read back into its port's
// list (only identify, for a partial one). A port not read yet is left alone.
func rdmStoreFixture(ip string, port int, f rdmFixture) {
	rdmStore.Lock()
	s := rdmStore.m[ip]
	known := s != nil && s.Read[port] != nil
	rdmStore.Unlock()
	if !known {
		return
	}
	rdmChange(ip, func(s *rdmNodeState) {
		r := s.Read[port]
		if r == nil {
			return
		}
		list := append([]rdmFixture(nil), r.Fixtures...)
		for i := range list {
			if list[i].UID != f.UID {
				continue
			}
			if f.Partial {
				list[i].Identify = f.Identify
			} else {
				g := f
				g.Overlaps, g.PastEnd = nil, false
				list[i] = g
			}
			markOverlaps(list)
			r.Fixtures = list
			return
		}
	})
}

// pushTo sends msg to every page subscribed to ip on /api/live.
func pushTo(ip string, msg obj) {
	pollers.Lock()
	p := pollers.m[ip]
	pollers.Unlock()
	if p == nil {
		return
	}
	for _, c := range p.subscribers() {
		c.send(msg)
	}
}
