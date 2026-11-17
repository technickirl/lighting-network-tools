package nodeconfig

// Browser WebSocket at /api/live (same messages as v28): the page sends
// {type:"subscribe", ip} / {type:"unsubscribe"}; the server polls the device's
// IOs and DMX port state every 3 s and pushes {type:"update", ip, ...} to every
// subscriber. A poller stops as soon as it has no subscribers, or after three
// consecutive failures (then it sends {type:"error", ip, message}).

import (
	"encoding/json"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

const pollEvery = 3 * time.Second

// lostAfter is how many missed polls in a row count as the device being away.
const lostAfter = 3

// wsSendTimeout is how long one page may take to accept an update.
const wsSendTimeout = 5 * time.Second

type wsClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (c *wsClient) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// A page that has gone away without closing (an iPad out of range or asleep)
	// stops taking data. Without a limit the send would wait on it for minutes,
	// and with it every other page watching this node. Closing ends its reader,
	// which unsubscribes it; the page reconnects when it is back.
	_ = c.conn.SetWriteDeadline(time.Now().Add(wsSendTimeout))
	if err := websocket.Message.Send(c.conn, string(data)); err != nil {
		c.conn.Close()
	}
}

type poller struct {
	ip   string
	subs map[*wsClient]bool
	stop chan struct{}
	fail int
	last map[string]string // serialised processBlocks / allIOs from the previous tick
}

var pollers = struct {
	sync.Mutex
	m map[string]*poller
}{m: map[string]*poller{}}

func subscribe(ip string, c *wsClient) {
	pollers.Lock()
	defer pollers.Unlock()
	p := pollers.m[ip]
	if p == nil {
		p = &poller{ip: ip, subs: map[*wsClient]bool{}, stop: make(chan struct{})}
		pollers.m[ip] = p
		go p.run()
		luminex.Logf("poller started for %s", ip)
	}
	p.subs[c] = true
}

func unsubscribe(ip string, c *wsClient) {
	pollers.Lock()
	defer pollers.Unlock()
	p := pollers.m[ip]
	if p == nil {
		return
	}
	delete(p.subs, c)
	if len(p.subs) == 0 {
		p.stopLocked("no subscribers")
	}
}

// stopLocked must be called with pollers locked.
func (p *poller) stopLocked(why string) {
	if pollers.m[p.ip] == p {
		delete(pollers.m, p.ip)
		close(p.stop)
		luminex.Logf("poller stopped for %s — %s", p.ip, why)
	}
}

func stopPoller(ip string) {
	pollers.Lock()
	defer pollers.Unlock()
	if p := pollers.m[ip]; p != nil {
		p.stopLocked("cache cleared")
	}
}

func stopAllPollers() {
	pollers.Lock()
	defer pollers.Unlock()
	for _, p := range pollers.m {
		p.stopLocked("all caches cleared")
	}
}

func (p *poller) subscribers() []*wsClient {
	pollers.Lock()
	defer pollers.Unlock()
	out := make([]*wsClient, 0, len(p.subs))
	for c := range p.subs {
		out = append(out, c)
	}
	return out
}

func (p *poller) run() {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
		}
		if isLocked, _ := locked(p.ip); isLocked {
			continue // a job has this device; it may be rebooting
		}
		update, err := fetchLiveUpdate(p.ip)
		if err != nil {
			// Keep trying while anyone is subscribed: a node rebooting (REBOOT,
			// a profile recall, a power cut) is away for longer than three
			// ticks, and giving up here left the page on ERROR until the
			// operator pressed CONNECT. The page is told once, then again when
			// the node is back.
			p.fail++
			if p.fail <= lostAfter || p.fail%20 == 0 {
				luminex.Errorf("poll error for %s (%d): %v", p.ip, p.fail, err)
			}
			if p.fail == lostAfter {
				for _, c := range p.subscribers() {
					c.send(obj{"type": "error", "ip": p.ip, "message": "Device unreachable, still trying"})
				}
			}
			continue
		}
		if p.fail >= lostAfter {
			luminex.Logf("poll for %s: device back after %d misses", p.ip, p.fail)
			p.last = nil // send the blocks and IOs again, whatever they were before
		}
		p.fail = 0
		msg := obj{"type": "update", "ip": p.ip}
		for k, v := range update {
			msg[k] = v
		}
		// The DMX port counters change every tick; the blocks and IOs rarely
		// do. Send those only when their resolved view changed, so the page
		// does not redraw the Process and IOs pages (and their buttons) every
		// 3 s for nothing.
		p.dropUnchanged(msg, "processBlocks")
		if !p.dropUnchanged(msg, "allIOs") {
			delete(msg, "dmxIOs")
			delete(msg, "sacnIOs")
		}
		for _, c := range p.subscribers() {
			c.send(msg)
		}
	}
}

// dropUnchanged removes msg[key] when it serialises to the same JSON as last
// tick and returns whether it changed. The first tick after subscribing
// always counts as changed.
func (p *poller) dropUnchanged(msg obj, key string) bool {
	v, ok := msg[key]
	if !ok {
		return false
	}
	data, err := json.Marshal(v)
	if err != nil {
		return true
	}
	if p.last == nil {
		p.last = map[string]string{}
	}
	if p.last[key] == string(data) {
		delete(msg, key)
		return false
	}
	p.last[key] = string(data)
	return true
}

func wsHandler(conn *websocket.Conn) {
	c := &wsClient{conn: conn}
	subscribed := ""
	defer func() {
		if subscribed != "" {
			unsubscribe(subscribed, c)
		}
		conn.Close()
	}()
	for {
		var raw string
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			return
		}
		var msg struct {
			Type string `json:"type"`
			IP   string `json:"ip"`
		}
		if json.Unmarshal([]byte(raw), &msg) != nil {
			continue
		}
		switch {
		case msg.Type == "subscribe" && validHost(msg.IP):
			if subscribed != "" {
				unsubscribe(subscribed, c)
			}
			subscribed = msg.IP
			subscribe(msg.IP, c)
			c.send(obj{"type": "subscribed", "ip": msg.IP})
		case msg.Type == "unsubscribe" && subscribed != "":
			unsubscribe(subscribed, c)
			subscribed = ""
			c.send(obj{"type": "unsubscribed"})
		}
	}
}
