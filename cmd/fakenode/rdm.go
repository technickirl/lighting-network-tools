package main

// The fake node's RDM: fixtures on DMX port 1 and on the ports fed by sACN U101, answered over Art-Net
// (internal/artnet's fake) on --artnet-port, and the REST calls a real LumiNode
// 12 (v2.9.1) offers for RDM. As on the real node, turning rdm on for a DMX IO
// sets its rdm_universe to the universe of the input feeding the port, and
// Art-Net RDM to that port-address only answers while rdm is on.

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/technickirl/lighting-network-tools/internal/artnet"
)

// fx is a fake Creamsource fixture with the settings, sensors and hours a real Vortex 8S reported.
func fx(uid artnet.UID, model string, start uint16, pers uint8, label string, modes ...artnet.FakePersonality) *artnet.FakeFixture {
	ids := map[string]uint16{"Vortex 8S": 0x0301, "Vortex 4": 0x0302, "Vortex 24": 0x0303} // one model ID per model
	return (&artnet.FakeFixture{UID: uid, Manufacturer: "Creamsource", Model: model, ModelID: ids[model], Category: 0x0101,
		Label: label, Software: "3.1.0", SoftwareID: 0x00030100, Start: start, Personality: pers, Personalities: modes}).WithVortexParams()
}

var (
	direct = artnet.FakePersonality{Footprint: 10, Name: "Direct 8bit"}
	hsi    = artnet.FakePersonality{Footprint: 16, Name: "HSI 16bit"}
	// Fixtures by the sACN universe (= Art-Net port-address) feeding their port.
	rdmFixtures = map[artnet.PortAddress][]*artnet.FakeFixture{
		1: {
			fx(0x4F75000DC113, "Vortex 8S", 1, 1, "CSV-8S 901395", direct, hsi),
			fx(0x4F75000DC114, "Vortex 8S", 11, 2, "CSV-8S 901396", direct, hsi), // 11-26: overlaps the next one
			fx(0x4F75000DC115, "Vortex 4", 21, 1, "CSV-4 900112", direct, hsi),
			{UID: 0x7A70000A0001, Manufacturer: "Fake Atmos", Model: "Hazer H1", ModelID: 0x0010, Category: 0x0401, Label: "Haze SL",
				Software: "1.0.4", Start: 501, Personality: 1, Personalities: []artnet.FakePersonality{{Footprint: 2, Name: "2ch"}}, AckTimerOnce: true},
			// Says what its channels are (SLOT_INFO and SLOT_DESCRIPTION): a CMY spot with a 16-bit dimmer.
			{UID: 0x7A70000B0001, Manufacturer: "Fake Movers", Model: "Spot S1", ModelID: 0x0020, Category: 0x0102, Label: "Spot 1",
				Software: "2.0.0", Start: 301, Personality: 1, Personalities: []artnet.FakePersonality{{Footprint: 8, Name: "Basic"}},
				Slots: []artnet.Slot{{Offset: 0, Label: 0x0001}, {Offset: 1, Type: 1, Label: 0}, {Offset: 2, Label: 0x0202}, {Offset: 3, Label: 0x0204},
					{Offset: 4, Label: 0x0203}, {Offset: 5, Label: 0x0101}, {Offset: 6, Label: 0x0102}, {Offset: 7, Label: 0xFFFF}},
				SlotNames: map[uint16]string{0: "Dimmer", 1: "Dimmer fine", 2: "Cyan", 3: "Magenta", 4: "Yellow", 5: "Pan", 6: "Tilt", 7: "Strobe / shutter"}},
		},
		// Ports 2 and 3 of the 4-port seed are both fed by sACN U101 (the BACKUP and HTP blocks).
		101: {fx(0x4F75000DC200, "Vortex 24", 1, 1, "CSV-24 911001", direct, hsi)},
	}
)

func uidText(u artnet.UID) string {
	b := u.Bytes()
	parts := make([]string, len(b))
	for i, c := range b {
		parts[i] = fmt.Sprintf("%02X", c)
	}
	return strings.Join(parts, ":")
}

// portInputUniverse is the universe of the network input feeding DMX port p, or -1.
func portInputUniverse(p int) int {
	key := strconv.Itoa(dmxIOBase + p)
	for _, b := range pbs {
		if outs, ok := b["outputs"].(obj); ok {
			if _, ok := outs[key]; !ok {
				continue
			}
			if ins, ok := b["inputs"].(obj); ok {
				if in := findIO(int(toF(ins["0"]))); in != nil && (in["io_class"] == "sacn" || in["io_class"] == "artnet") {
					return int(toF(in["universe"]))
				}
			}
		}
	}
	return -1
}

func portFeed(p int) obj {
	key := strconv.Itoa(dmxIOBase + p)
	for _, b := range pbs {
		if outs, ok := b["outputs"].(obj); ok {
			if _, ok := outs[key]; ok {
				if ins, ok := b["inputs"].(obj); ok {
					return findIO(int(toF(ins["0"])))
				}
			}
		}
	}
	return nil
}

// afterIOPut does what the node does when a DMX IO's rdm turns on (was: whether it was on before).
func afterIOPut(io obj, was bool) {
	if io["io_class"] != "dmx" {
		return
	}
	port := int(toF(io["port_number"]))
	if io["rdm"] == true {
		io["rdm_universe"] = portInputUniverse(port)
		if !was {
			startSearch(port, true)
		}
	} else {
		delete(searches, port)
	}
}

// A real node's RDM search (LumiNode 12, v2.9.1, 14 Nov 2026): after RDM goes on the
// node lists nothing until the search is done, then everything it found at once;
// a forced discovery keeps the old list until it is done, then swaps in the new
// one. It does not pick up a fixture that powers up later by itself. With
// --rdm-search above 0 the fake does the same, and the last fixture on each port
// is still powering up during the first search, so only a Rediscover finds it.
var (
	rdmSearch time.Duration
	searches  = map[int]*portSearch{} // DMX port (0-based) → its search; guarded by mu
)

type portSearch struct {
	shown, next map[artnet.UID]bool
	at          time.Time
}

// startSearch starts an RDM search on a DMX port; first is the one after RDM goes on. Call with mu held.
func startSearch(port int, first bool) {
	if rdmSearch <= 0 {
		return
	}
	_, list := rdmAt(port)
	next := map[artnet.UID]bool{}
	for i, f := range list {
		if first && i == len(list)-1 && len(list) > 1 {
			continue // still powering up
		}
		next[f.UID] = true
	}
	s := &portSearch{next: next, at: time.Now().Add(rdmSearch)}
	if !first { // the old list stays until the search is done
		if s.shown = found(port); s.shown == nil {
			s.shown = map[artnet.UID]bool{}
			for _, f := range list {
				s.shown[f.UID] = true
			}
		}
	}
	searches[port] = s
}

// found is the UIDs the node lists for a DMX port now (nil: every fixture on it). Call with mu held.
func found(port int) map[artnet.UID]bool {
	s := searches[port]
	if rdmSearch <= 0 || s == nil {
		return nil
	}
	if !time.Now().Before(s.at) {
		s.shown = s.next
	}
	if s.shown == nil {
		return map[artnet.UID]bool{}
	}
	return s.shown
}

// rdmAt is the fixtures a DMX port answers for now: none while its rdm is off.
func rdmAt(port int) (artnet.PortAddress, []*artnet.FakeFixture) {
	io := findIO(dmxIOBase + port)
	if io == nil || io["rdm"] != true {
		return 0, nil
	}
	pa := artnet.PortAddress(toF(io["rdm_universe"]))
	return pa, rdmFixtures[pa]
}

func startArtnet(ip string, port int) {
	if port <= 0 {
		return
	}
	enabled := func(pa artnet.PortAddress) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, io := range ios {
			if io["io_class"] == "dmx" && io["rdm"] == true && artnet.PortAddress(toF(io["rdm_universe"])) == pa {
				return true
			}
		}
		return false
	}
	f, err := artnet.ServeFake(net.JoinHostPort(ip, strconv.Itoa(port)), rdmFixtures, enabled)
	if err != nil {
		log.Printf("Art-Net RDM not started: %v", err)
		return
	}
	// ArtTodRequest lists what the node has found, from every port on that universe.
	f.SetListed(func(pa artnet.PortAddress) map[artnet.UID]bool {
		mu.Lock()
		defer mu.Unlock()
		var out map[artnet.UID]bool
		for p := 0; p < nPorts; p++ {
			at, _ := rdmAt(p)
			seen := found(p)
			if at != pa || seen == nil {
				continue
			}
			if out == nil {
				out = map[artnet.UID]bool{}
			}
			for u := range seen {
				out[u] = true
			}
		}
		return out
	})
	log.Printf("Art-Net RDM on udp %s (run the app with DEVICE_ARTNET_PORT=%d)", f.Addr(), port)
}

func rdmRoutes(h func(string, func(http.ResponseWriter, *http.Request))) {
	h("GET /api/IO/{idx}/artrdm_candidates", func(w http.ResponseWriter, r *http.Request) {
		io := findIO(idx(r.PathValue("idx")))
		if io == nil || io["io_class"] != "dmx" {
			bad(w, 400, "only a DMX IO has ArtRdm candidates")
			return
		}
		out := []obj{}
		if in := portFeed(int(toF(io["port_number"]))); in != nil {
			out = append(out, in)
		}
		writeJSON(w, 200, out)
	})
	h("GET /api/rdm/{port}/connected_devices", func(w http.ResponseWriter, r *http.Request) {
		port := idx(r.PathValue("port"))
		_, list := rdmAt(port)
		seen := found(port)
		out := []string{}
		for _, f := range list {
			if seen == nil || seen[f.UID] {
				out = append(out, uidText(f.UID))
			}
		}
		writeJSON(w, 200, out)
	})
	// What the node has overheard: everything about the fixtures on ports with rdm on.
	h("GET /api/rdm/uid", func(w http.ResponseWriter, r *http.Request) {
		out := []obj{}
		for p := 0; p < nPorts; p++ {
			if q := r.URL.Query().Get("port"); q != "" && q != strconv.Itoa(p) {
				continue
			}
			_, list := rdmAt(p)
			for _, f := range list {
				m := f.Personalities[f.Personality-1]
				d := obj{"port": p, "uid": uidText(f.UID), "manufacturer": int(f.UID.Manufacturer()), "manufacturer_label": f.Manufacturer,
					"device_model": f.ModelID, "device_model_description": f.Model, "device_label": f.Label, "software_version_label": f.Software,
					"software_version": f.SoftwareID, "product_category": f.Category, "dmx_footprint": m.Footprint, "dmx_personality": f.Personality,
					"personality_count": len(f.Personalities), "dmx_personality_description": []any{}, "dmx_personality_footprint": []any{},
					"dmx_start_address": f.Start, "sub_device_count": 0, "sensor_count": f.Sensors}
				out = append(out, d)
			}
		}
		writeJSON(w, 200, out)
	})
	h("POST /api/dmx/ports/{port}/force_discovery", func(w http.ResponseWriter, r *http.Request) {
		// A real LumiNode 12 (v2.9.1) answers 415 to this without a JSON body.
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			bad(w, 415, "The server does not support the media type transmitted in the request")
			return
		}
		if port := idx(r.PathValue("port")); port >= 0 && port < nPorts {
			if io := findIO(dmxIOBase + port); io != nil && io["rdm"] == true {
				startSearch(port, false)
			}
		}
		w.WriteHeader(204)
	})
}
