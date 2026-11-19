package artnet

import (
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

// ErrNoTod means the node did not answer an ArtTodRequest for that port-address:
// RDM is off on the port, or nothing on the node uses that address.
var ErrNoTod = errors.New("the node did not answer the RDM device list request")

// ErrNoReply means an RDM device did not answer, after one retry.
var ErrNoReply = errors.New("no RDM reply")

// Controller sends Art-Net RDM requests and matches the replies. One per
// process: it owns the Art-Net UDP port.
type Controller struct {
	conn       *net.UDPConn
	devicePort int
	src        UID

	mu      sync.Mutex
	waiters map[int]*waiter
	next    int
	tn      byte
	lines   map[string]*sync.Mutex // one RDM transaction at a time per node

	// Timeout is how long to wait for each RDM reply (one retry follows).
	Timeout time.Duration
}

type waiter struct {
	from string
	op   uint16
	ch   chan []byte
}

// Open binds listen (":6454" for real use) and sends to devicePort on each node.
func Open(listen string, devicePort int) (*Controller, error) {
	addr, err := net.ResolveUDPAddr("udp4", listen)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) || isAddrInUse(err) {
			return nil, fmt.Errorf("Art-Net port %d is in use by another program on this computer (close any other Art-Net software): %v", addr.Port, err)
		}
		return nil, fmt.Errorf("cannot open Art-Net port %d: %v", addr.Port, err)
	}
	c := &Controller{conn: conn, devicePort: devicePort, src: controllerUID(), waiters: map[int]*waiter{},
		lines: map[string]*sync.Mutex{}, Timeout: time.Second}
	go c.read()
	luminex.Logf("Art-Net RDM controller on %s, UID %s", conn.LocalAddr(), c.src)
	return c, nil
}

func isAddrInUse(err error) bool {
	var se *os.SyscallError
	if errors.As(err, &se) && errors.Is(se.Err, syscall.EADDRINUSE) {
		return true
	}
	// Windows says WSAEADDRINUSE (10048), which is not syscall.EADDRINUSE there.
	msg := err.Error()
	return strings.Contains(msg, "address already in use") || strings.Contains(msg, "Only one usage of each socket address")
}

// controllerUID is in 0x7FF0-0x7FFF, which ESTA keeps for development, made
// from the host name so it stays the same between runs.
func controllerUID() UID {
	h, _ := os.Hostname()
	return UID(0x7FF0)<<32 | UID(crc32.ChecksumIEEE([]byte("luminex-work "+h)))
}

// UID is the controller's own RDM UID.
func (c *Controller) UID() UID { return c.src }

// LocalAddr is the address the controller listens on.
func (c *Controller) LocalAddr() net.Addr { return c.conn.LocalAddr() }

// Close releases the port.
func (c *Controller) Close() error { return c.conn.Close() }

func (c *Controller) read() {
	buf := make([]byte, 2048)
	for {
		n, from, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		op, ok := Opcode(buf[:n])
		if !ok {
			continue
		}
		pkt := append([]byte(nil), buf[:n]...)
		ip := from.IP.String()
		c.mu.Lock()
		for _, w := range c.waiters {
			if w.op == op && w.from == ip {
				select {
				case w.ch <- pkt:
				default:
				}
			}
		}
		c.mu.Unlock()
	}
}

func (c *Controller) listen(ip string, op uint16) (<-chan []byte, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.next
	c.next++
	w := &waiter{from: ip, op: op, ch: make(chan []byte, 256)}
	c.waiters[id] = w
	return w.ch, func() {
		c.mu.Lock()
		delete(c.waiters, id)
		c.mu.Unlock()
	}
}

func (c *Controller) send(ip string, pkt []byte, what string) error {
	addr, err := resolve(ip, c.devicePort)
	if err != nil {
		return err
	}
	luminex.Logf("→ Art-Net %s:%d %s", ip, c.devicePort, what)
	_, err = c.conn.WriteToUDP(pkt, addr)
	if err != nil {
		luminex.Logf("✗ Art-Net %s: %v", ip, err)
	}
	return err
}

func resolve(ip string, port int) (*net.UDPAddr, error) {
	a, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("bad node address %q: %v", ip, err)
	}
	return a, nil
}

// Poll sends an ArtPoll to one node and collects its replies for wait.
func (c *Controller) Poll(ip string, wait time.Duration) ([]PollReply, error) {
	ch, done := c.listen(ip, OpPollReply)
	defer done()
	if err := c.send(ip, PollPacket(), "ArtPoll"); err != nil {
		return nil, err
	}
	var out []PollReply
	for end := time.After(wait); ; {
		select {
		case b := <-ch:
			if r, err := ParsePollReply(b); err == nil {
				out = append(out, r)
			}
		case <-end:
			luminex.Logf("← Art-Net %s %d ArtPollReply", ip, len(out))
			return out, nil
		}
	}
}

// Tod asks a node for the RDM devices it has found at one port-address. It
// returns once one table is complete, or after wait (merging what came when
// several DMX ports answer for the address).
func (c *Controller) Tod(ip string, pa PortAddress, wait time.Duration) ([]UID, error) {
	tables, err := c.TodTables(ip, pa, 1, wait)
	if err != nil {
		return nil, err
	}
	seen := map[UID]bool{}
	var all []UID
	for _, t := range tables {
		for _, u := range t.UIDs {
			if !seen[u] {
				seen[u] = true
				all = append(all, u)
			}
		}
	}
	return all, nil
}

// TodTable is one DMX port's answer to an ArtTodRequest.
type TodTable struct {
	Bind int // its BindIndex, which does not say which port it is
	UIDs []UID
}

// TodTables asks a node for the RDM devices at one port-address and keeps each
// port's table apart. Two DMX ports fed from the same universe share a
// port-address, and each answers with its own table (two ports on sACN U35, 13
// Nov 2026). Their BindIndex is not the port number: LumiNode 12 - 7 (v2.9.1)
// sent port 2's table as 2, LumiNode 12 - 4 (v2.9.1, U33, 16 Nov 2026) sent
// port 1's as 2 and port 2's as 255, and LumiNode 12 - 5 (U19, 19 Nov 2026) sent
// both ports' as 255. So a table is also told apart by its blocks: a block
// number it already has starts another table. A port with nothing on its line
// may send none. It returns once want tables are complete, or after wait with
// what came.
func (c *Controller) TodTables(ip string, pa PortAddress, want int, wait time.Duration) ([]TodTable, error) {
	ch, done := c.listen(ip, OpTodData)
	defer done()
	if err := c.send(ip, TodRequestPacket(pa), "ArtTodRequest "+pa.String()); err != nil {
		return nil, err
	}
	type table struct {
		bind, port int
		blocks     map[int]bool
		seen       map[UID]bool
		uids       []UID
		total      int
	}
	var tables []*table
	result := func() ([]TodTable, error) {
		if len(tables) == 0 {
			return nil, ErrNoTod
		}
		out := make([]TodTable, 0, len(tables))
		for _, tb := range tables {
			out = append(out, TodTable{Bind: tb.bind, UIDs: tb.uids})
		}
		return out, nil
	}
	for end := time.After(wait); ; {
		select {
		case b := <-ch:
			t, err := ParseTodData(b)
			if err != nil || t.Address != pa {
				continue
			}
			luminex.Logf("← Art-Net %s ArtTodData %s bind %d port %d total %d block %d: %v", ip, pa, t.BindIndex, t.Port, t.UIDTotal, t.BlockCount, t.UIDs)
			if t.Nak {
				return nil, fmt.Errorf("the node refused the RDM device list request for %s", pa)
			}
			var tb *table
			for _, x := range tables {
				if x.bind == t.BindIndex && x.port == t.Port && !x.blocks[t.BlockCount] {
					tb = x
					break
				}
			}
			if tb == nil {
				tb = &table{bind: t.BindIndex, port: t.Port, blocks: map[int]bool{}, seen: map[UID]bool{}}
				tables = append(tables, tb)
			}
			tb.blocks[t.BlockCount] = true
			tb.total = t.UIDTotal
			for _, u := range t.UIDs {
				if !tb.seen[u] {
					tb.seen[u] = true
					tb.uids = append(tb.uids, u)
				}
			}
			complete := 0
			for _, x := range tables {
				if len(x.uids) >= x.total {
					complete++
				}
			}
			if complete >= max(want, 1) {
				return result()
			}
		case <-end:
			return result() // some blocks may be missing; use what came
		}
	}
}

func (c *Controller) line(ip string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.lines[ip]
	if m == nil {
		m = &sync.Mutex{}
		c.lines[ip] = m
	}
	return m
}

// Get sends an RDM GET to a device behind a node's port and returns its data.
// It follows ACK_TIMER (asking for the queued reply) and ACK_OVERFLOW (asking
// again and joining the parts).
func (c *Controller) Get(ip string, pa PortAddress, dev UID, pid uint16, data []byte) ([]byte, error) {
	return c.Do(ip, pa, Request{Dest: dev, CC: CCGet, PID: pid, Data: data})
}

// Set sends an RDM SET and waits for the device to accept it. Under DRY_RUN it
// is logged, not sent, and returns luminex.ErrDryRun.
func (c *Controller) Set(ip string, pa PortAddress, dev UID, pid uint16, data []byte) error {
	_, err := c.Do(ip, pa, Request{Dest: dev, CC: CCSet, PID: pid, Data: data})
	return err
}

// Do sends one RDM request. Under DRY_RUN a SET is logged and not sent.
func (c *Controller) Do(ip string, pa PortAddress, req Request) ([]byte, error) {
	req.Src = c.src
	if req.Dest.Broadcast() {
		return nil, fmt.Errorf("%s is a broadcast UID: RDM requests go to one fixture at a time", req.Dest)
	}
	if req.CC == CCSet && luminex.DryRun {
		luminex.Logf("→ Art-Net %s RDM SET %s to %s data=%x", ip, PIDName(req.PID), req.Dest, req.Data)
		luminex.Logf("  DRY RUN — not sent")
		return nil, luminex.ErrDryRun
	}
	l := c.line(ip)
	l.Lock()
	defer l.Unlock()

	var joined []byte
	for parts := 0; parts < 16; parts++ {
		resp, err := c.roundTrip(ip, pa, req)
		if err != nil {
			return nil, err
		}
		for tries := 0; resp.Type == RespAckTimer && tries < 10; tries++ {
			delay := 100 * time.Millisecond
			if len(resp.Data) >= 2 {
				delay = time.Duration(int(resp.Data[0])<<8|int(resp.Data[1])) * 100 * time.Millisecond
			}
			time.Sleep(min(max(delay, 50*time.Millisecond), 3*time.Second))
			q := Request{Dest: req.Dest, Src: c.src, SubDevice: req.SubDevice, CC: CCGet, PID: PIDQueuedMessage, Data: []byte{0x02}}
			if resp, err = c.roundTrip(ip, pa, q); err != nil {
				return nil, err
			}
			if resp.Type != RespAckTimer && resp.PID != req.PID {
				return nil, fmt.Errorf("%s: device sent %s while its reply was queued", PIDName(req.PID), PIDName(resp.PID))
			}
		}
		switch resp.Type {
		case RespAck:
			return append(joined, resp.Data...), nil
		case RespAckOverflow:
			joined = append(joined, resp.Data...)
		case RespNack:
			reason := uint16(0xFFFF)
			if len(resp.Data) >= 2 {
				reason = uint16(resp.Data[0])<<8 | uint16(resp.Data[1])
			}
			return nil, &NackError{PID: req.PID, Reason: reason}
		default:
			return nil, fmt.Errorf("%s: response type %d", PIDName(req.PID), resp.Type)
		}
	}
	return nil, fmt.Errorf("%s: reply still overflowing after 16 parts", PIDName(req.PID))
}

// roundTrip sends a request and waits for the matching response, retrying once.
func (c *Controller) roundTrip(ip string, pa PortAddress, req Request) (Response, error) {
	ch, done := c.listen(ip, OpRdm)
	defer done()
	for attempt := 0; attempt < 2; attempt++ {
		c.mu.Lock()
		c.tn++
		req.TN = c.tn
		c.mu.Unlock()
		msg := req.Encode()
		cc := "GET"
		if req.CC == CCSet {
			cc = "SET"
		}
		if err := c.send(ip, RdmPacket(pa, msg), fmt.Sprintf("ArtRdm %s %s %s to %s tn=%d data=%x", pa, cc, PIDName(req.PID), req.Dest, req.TN, req.Data)); err != nil {
			return Response{}, err
		}
		for end := time.After(c.Timeout); ; {
			var b []byte
			select {
			case b = <-ch:
			case <-end:
				luminex.Logf("✗ Art-Net %s no reply to %s from %s (tn=%d)", ip, PIDName(req.PID), req.Dest, req.TN)
			}
			if b == nil {
				break
			}
			gotPA, m, err := ParseRdmPacket(b)
			if err != nil || gotPA != pa {
				continue
			}
			resp, err := ParseResponse(m)
			if err != nil {
				luminex.Logf("← Art-Net %s bad RDM reply: %v (%s)", ip, err, hex.EncodeToString(m))
				continue
			}
			if resp.Dest != c.src || resp.Src != req.Dest || resp.TN != req.TN {
				continue // another controller's traffic, or a late reply
			}
			luminex.Logf("← Art-Net %s RDM %s from %s type=%d data=%x", ip, PIDName(resp.PID), resp.Src, resp.Type, resp.Data)
			return resp, nil
		}
	}
	return Response{}, fmt.Errorf("%s from %s: %w", PIDName(req.PID), req.Dest, ErrNoReply)
}

// The process-wide controller, opened on first use. A failed open (the port
// in use) is retried, at most every few seconds, so closing the other program
// is enough.
var shared struct {
	sync.Mutex
	c       *Controller
	err     error
	tried   time.Time
	envOnce sync.Once
	listen  string
	devPort int
}

// Shared returns the process's controller, opening it if needed. It listens on
// 6454 and sends to 6454 unless ARTNET_LISTEN_PORT or DEVICE_ARTNET_PORT say
// otherwise (both for the test fakes only).
func Shared() (*Controller, error) {
	shared.Lock()
	defer shared.Unlock()
	shared.envOnce.Do(func() {
		shared.listen, shared.devPort = fmt.Sprintf(":%d", Port), Port
		if p := os.Getenv("ARTNET_LISTEN_PORT"); p != "" {
			luminex.Warnf("ARTNET_LISTEN_PORT=%s set — listening for Art-Net on that port instead of %d (testing only)", p, Port)
			shared.listen = ":" + p
		}
		if p, err := strconv.Atoi(os.Getenv("DEVICE_ARTNET_PORT")); err == nil && p > 0 {
			luminex.Warnf("DEVICE_ARTNET_PORT=%d set — sending Art-Net to that port instead of %d (testing only)", p, Port)
			shared.devPort = p
		}
	})
	if shared.c != nil {
		return shared.c, nil
	}
	if shared.err != nil && time.Since(shared.tried) < 3*time.Second {
		return nil, shared.err
	}
	shared.tried = time.Now()
	shared.c, shared.err = Open(shared.listen, shared.devPort)
	if shared.err != nil {
		luminex.Errorf("%v", shared.err)
	}
	return shared.c, shared.err
}

// SetSharedForTests makes Shared return c (nil clears it).
func SetSharedForTests(c *Controller) {
	shared.Lock()
	defer shared.Unlock()
	shared.envOnce.Do(func() {})
	shared.c, shared.err = c, nil
}
