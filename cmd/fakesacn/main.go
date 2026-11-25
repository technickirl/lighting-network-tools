// fakesacn is a pretend console for trying the sACN view without one: it sends
// moving levels on a few universes and announces them (universe discovery).
//
//	go run ./cmd/fakesacn                         universes 1-4 to this computer only
//	go run ./cmd/fakesacn --universes 1-2,9 --name Backup --priority 90
//	go run ./cmd/fakesacn --multicast             onto the network, like a real console
//
// By default the packets go to 127.0.0.1 and never leave this computer. With
// --multicast (or --to <address>) they go out for real, and any node listening
// to those universes will put the levels on its DMX outputs: do not use it on a
// network with fixtures you do not mean to drive.
package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/technickirl/lighting-network-tools/internal/sacn"
)

func main() {
	list := flag.String("universes", "1-4", "universes to send, such as 1-4,9")
	name := flag.String("name", "Fake console", "source name")
	priority := flag.Int("priority", 100, "sACN priority, 0-200")
	to := flag.String("to", "127.0.0.1", "address to send to")
	multicast := flag.Bool("multicast", false, "send to each universe's multicast group instead (goes onto the network)")
	rate := flag.Int("rate", 40, "packets a second for each universe")
	flag.Parse()

	universes, err := sacn.ParseUniverses(*list)
	if err != nil || len(universes) == 0 || *priority < 0 || *priority > 200 || *rate < 1 || *rate > 44 {
		fmt.Fprintln(os.Stderr, "fakesacn: need universes like 1-4, a priority of 0-200 and a rate of 1-44:", err)
		os.Exit(2)
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakesacn:", err)
		os.Exit(1)
	}
	dest := func(u uint16) *net.UDPAddr {
		if *multicast {
			return &net.UDPAddr{IP: sacn.GroupAddr(u), Port: sacn.Port}
		}
		return &net.UDPAddr{IP: net.ParseIP(*to), Port: sacn.Port}
	}
	if !*multicast && net.ParseIP(*to) == nil {
		fmt.Fprintln(os.Stderr, "fakesacn: --to needs an IP address")
		os.Exit(2)
	}
	var cid sacn.CID
	_, _ = rand.Read(cid[:])
	where := *to
	if *multicast {
		where = "multicast: THIS GOES ONTO THE NETWORK"
	}
	fmt.Printf("fakesacn: %q, priority %d, universes %s, %d Hz, to %s\n", *name, *priority, sacn.FormatUniverses(universes), *rate, where)

	announce := func() {
		_, _ = conn.WriteToUDP(sacn.Discovery{CID: cid, Source: *name, Universes: universes}.Marshal(), dest(sacn.DiscoveryUniverse))
	}
	announce()
	tick := time.NewTicker(time.Second / time.Duration(*rate))
	every10 := time.NewTicker(10 * time.Second)
	start := time.Now()
	slots := make([]byte, 512)
	for seq := byte(0); ; seq++ {
		select {
		case <-every10.C:
			announce()
		case <-tick.C:
		}
		t := time.Since(start).Seconds()
		for i, u := range universes {
			// A slow wave across the universe, a chase on channels 1-24 and the universe's number on 512.
			for ch := range slots {
				slots[ch] = byte(127.5 + 127.5*math.Sin(t*0.8+float64(ch)/20+float64(i)))
			}
			for ch := 0; ch < 24; ch++ {
				slots[ch] = 0
			}
			slots[int(t*4)%24] = 255
			slots[511] = byte(u)
			pkt := sacn.Data{CID: cid, Source: *name, Priority: uint8(*priority), Seq: seq, Universe: u, Slots: slots}.Marshal()
			if _, err := conn.WriteToUDP(pkt, dest(u)); err != nil {
				fmt.Fprintln(os.Stderr, "fakesacn: universe "+strconv.Itoa(int(u))+":", err)
			}
		}
	}
}
