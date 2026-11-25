package sacn

// The sender: this tool as an sACN source. It holds the levels of a few
// universes and, only while it is running, multicasts each of them 40 times a
// second from one chosen network interface. It never starts by itself. When it
// stops, or lets go of a universe, it says so (three packets with the
// Stream_Terminated bit), so receivers drop it at once instead of timing out.
//
// Under DRY_RUN it runs but writes nothing to the network.

import (
	"crypto/rand"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"golang.org/x/net/ipv4"

	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

const (
	// MaxOutput is how many universes the sender carries at once.
	MaxOutput = 32
	// DefaultPriority is sACN's own default.
	DefaultPriority = 100

	sendEvery     = 25 * time.Millisecond // 40 Hz
	announceEvery = 10 * time.Second      // universe discovery, E1.31 4.3
)

// Sender is the one sACN source of this process.
type Sender struct {
	mu       sync.Mutex
	cid      CID
	name     string
	priority uint8
	iface    string
	running  bool
	lastErr  string
	levels   map[uint16]*[512]byte
	seq      map[uint16]uint8
	conn     *net.UDPConn
	stop     chan struct{}
	done     chan struct{}
	// write sends one packet to a universe's group. Tests replace it.
	write func(u uint16, pkt []byte) error
}

// NewSender makes a stopped sender with a fresh CID.
func NewSender() *Sender {
	s := &Sender{name: "Luminex Commission", priority: DefaultPriority, levels: map[uint16]*[512]byte{}, seq: map[uint16]uint8{}}
	_, _ = rand.Read(s.cid[:])
	return s
}

// CID is this source's identifier, so the viewer can tell its own packets from another console's.
func (s *Sender) CID() CID { return s.cid }

// SenderState is the sender as the page shows it, without levels.
type SenderState struct {
	Running   bool   `json:"running"`
	Priority  int    `json:"priority"`
	Name      string `json:"name"`
	Interface string `json:"interface"`
	Error     string `json:"error,omitempty"`
}

func (s *Sender) State() SenderState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SenderState{Running: s.running, Priority: int(s.priority), Name: s.name, Interface: s.iface, Error: s.lastErr}
}

// Universes lists the universes the sender carries, in order.
func (s *Sender) Universes() []uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.universes()
}

func (s *Sender) universes() []uint16 {
	out := make([]uint16, 0, len(s.levels))
	for u := range s.levels {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Levels is a copy of one universe's 512 levels, and whether the sender carries it.
func (s *Sender) Levels(u uint16) ([512]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.levels[u]; l != nil {
		return *l, true
	}
	return [512]byte{}, false
}

// Ensure makes the sender carry a universe (at zero), if it does not already.
func (s *Sender) Ensure(u uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensure(u)
}

func (s *Sender) ensure(u uint16) error {
	if u < 1 || u > MaxUniverse {
		return fmt.Errorf("universes run from 1 to %d", MaxUniverse)
	}
	if s.levels[u] != nil {
		return nil
	}
	if len(s.levels) >= MaxOutput {
		return fmt.Errorf("this tool sends at most %d universes at once", MaxOutput)
	}
	s.levels[u] = &[512]byte{}
	return nil
}

// Set changes levels on a universe. Addresses are 1-512; the universe is added if new.
func (s *Sender) Set(u uint16, set map[int]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for addr := range set {
		if addr < 1 || addr > 512 {
			return fmt.Errorf("address %d: addresses run from 1 to 512", addr)
		}
	}
	if err := s.ensure(u); err != nil {
		return err
	}
	for addr, v := range set {
		s.levels[u][addr-1] = v
	}
	return nil
}

// Release stops carrying a universe, telling receivers so if the sender is running.
func (s *Sender) Release(u uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.levels[u] == nil {
		return
	}
	if s.running {
		s.terminate(u)
	}
	delete(s.levels, u)
	delete(s.seq, u)
}

func (s *Sender) packet(u uint16, options uint8) []byte {
	s.seq[u]++
	return Data{CID: s.cid, Source: s.name, Priority: s.priority, Seq: s.seq[u], Options: options, Universe: u, Slots: s.levels[u][:]}.Marshal()
}

func (s *Sender) send(u uint16, pkt []byte) {
	if luminex.DryRun || s.write == nil {
		return
	}
	if err := s.write(u, pkt); err != nil {
		if s.lastErr == "" {
			luminex.Warnf("sACN output: universe %d: %v", u, err)
		}
		s.lastErr = fmt.Sprintf("Could not send universe %d: %v", u, err)
		return
	}
	s.lastErr = ""
}

func (s *Sender) terminate(u uint16) {
	for i := 0; i < 3; i++ {
		s.send(u, s.packet(u, OptTerminated))
	}
}

func (s *Sender) announce() {
	us := s.universes()
	if len(us) > 0 {
		s.send(DiscoveryUniverse, Discovery{CID: s.cid, Source: s.name, Universes: us}.Marshal())
	}
}

// OutputInterface is a network interface the sender can use.
type OutputInterface struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// OutputInterfaces lists the interfaces that can multicast, each with its IPv4 address.
func OutputInterfaces() []OutputInterface {
	out := []OutputInterface{}
	ifis, _ := net.Interfaces()
	for _, ifi := range ifis {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				out = append(out, OutputInterface{Name: ifi.Name, Address: ipn.IP.To4().String()})
				break
			}
		}
	}
	return out
}

// Start begins sending from the named interface. The name and priority stay
// until the next Start.
func (s *Sender) Start(iface string, priority int, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("already sending")
	}
	if priority < 0 || priority > 200 {
		return fmt.Errorf("priority runs from 0 to 200")
	}
	if name == "" {
		name = "Luminex Commission"
	}
	if len(name) > 63 {
		return fmt.Errorf("the source name is at most 63 characters")
	}
	if s.write == nil || s.conn != nil {
		// A real socket (tests set write themselves and have no conn).
		ifi, err := net.InterfaceByName(iface)
		if err != nil {
			return fmt.Errorf("no network interface called %q on this computer", iface)
		}
		conn, err := net.ListenUDP("udp4", nil)
		if err != nil {
			return err
		}
		pc := ipv4.NewPacketConn(conn)
		if err := pc.SetMulticastInterface(ifi); err != nil {
			_ = conn.Close()
			return fmt.Errorf("cannot send from %s: %w", iface, err)
		}
		_ = pc.SetMulticastTTL(1)         // the lighting network only, never through a router
		_ = pc.SetMulticastLoopback(true) // so the sACN view on this computer sees it too
		s.conn = conn
		s.write = func(u uint16, pkt []byte) error {
			_, err := conn.WriteToUDP(pkt, &net.UDPAddr{IP: GroupAddr(u), Port: Port})
			return err
		}
	}
	s.iface, s.priority, s.name, s.lastErr = iface, uint8(priority), name, ""
	s.running = true
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	dry := ""
	if luminex.DryRun {
		dry = " (DRY RUN: nothing is sent)"
	}
	luminex.Logf("sACN output: START from %s as %q, priority %d, universes %s%s", iface, name, priority, FormatUniverses(s.universes()), dry)
	go s.run(s.stop, s.done)
	return nil
}

func (s *Sender) run(stop, done chan struct{}) {
	defer close(done)
	tick := time.NewTicker(sendEvery)
	defer tick.Stop()
	last := time.Time{}
	for {
		select {
		case <-stop:
			return
		case now := <-tick.C:
			s.mu.Lock()
			if now.Sub(last) >= announceEvery {
				s.announce()
				last = now
			}
			for _, u := range s.universes() {
				s.send(u, s.packet(u, 0))
			}
			s.mu.Unlock()
		}
	}
}

// Stop ends the output, telling receivers so. The levels are kept.
func (s *Sender) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.stop)
	done := s.done
	s.mu.Unlock()
	<-done
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.universes() {
		s.terminate(u)
	}
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn, s.write = nil, nil
	}
	luminex.Logf("sACN output: STOP")
}
