// Package sacn reads (and builds) streaming ACN, ANSI E1.31: DMX levels over UDP
// port 5568, one multicast group per universe. receiver.go listens and keeps what
// every source is sending; routes.go serves that to the page.
package sacn

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
)

const (
	// Port is the sACN port. Every device uses it.
	Port = 5568
	// DiscoveryUniverse is where sources list the universes they send (E1.31-2016).
	DiscoveryUniverse = 64214
	// MaxUniverse is the highest universe that carries levels.
	MaxUniverse = 63999

	vectorRootData     = 0x00000004
	vectorRootExtended = 0x00000008
	vectorFramingData  = 0x00000002
	vectorExtDiscovery = 0x00000002
	vectorDiscoveryLst = 0x00000001
	vectorDMPSet       = 0x02

	// Options bits of a data packet.
	OptPreview    = 0x80 // not for live output (a visualiser feed)
	OptTerminated = 0x40 // the source has stopped sending this universe

	dataHeader = 126 // octets before the first slot
)

var acnID = []byte{'A', 'S', 'C', '-', 'E', '1', '.', '1', '7', 0, 0, 0}

var errNotSACN = errors.New("not an sACN packet")

// CID is a source's component identifier: 16 octets, fixed for the life of the source.
type CID [16]byte

func (c CID) String() string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", c[0:4], c[4:6], c[6:8], c[8:10], c[10:16])
}

// Data is one E1.31 data packet: one universe's slots from one source.
type Data struct {
	CID       CID
	Source    string // the source's name, up to 63 octets of UTF-8
	Priority  uint8  // 0-200, default 100
	Sync      uint16 // synchronisation universe, 0 for none
	Seq       uint8
	Options   uint8
	Universe  uint16
	StartCode uint8  // 0 for levels, 0xDD for per-address priority
	Slots     []byte // up to 512
}

// Discovery is one page of a source's universe list.
type Discovery struct {
	CID       CID
	Source    string
	Page      uint8
	Last      uint8
	Universes []uint16
}

// GroupAddr is the multicast group of a universe: 239.255.<high>.<low>.
func GroupAddr(universe uint16) net.IP {
	return net.IPv4(239, 255, byte(universe>>8), byte(universe))
}

func flagsLength(n int) uint16 { return 0x7000 | uint16(n&0x0fff) }

func name64(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return strings.ToValidUTF8(string(b), "?")
}

func root(b []byte, vector uint32, cid CID) {
	binary.BigEndian.PutUint16(b[0:], 0x0010)
	copy(b[4:16], acnID)
	binary.BigEndian.PutUint16(b[16:], flagsLength(len(b)-16))
	binary.BigEndian.PutUint32(b[18:], vector)
	copy(b[22:38], cid[:])
}

// Marshal builds the packet. More than 512 slots are cut off.
func (d Data) Marshal() []byte {
	slots := d.Slots
	if len(slots) > 512 {
		slots = slots[:512]
	}
	b := make([]byte, dataHeader+len(slots))
	root(b, vectorRootData, d.CID)
	binary.BigEndian.PutUint16(b[38:], flagsLength(len(b)-38))
	binary.BigEndian.PutUint32(b[40:], vectorFramingData)
	copy(b[44:107], d.Source) // 64 octets, the last always 0
	b[108] = d.Priority
	binary.BigEndian.PutUint16(b[109:], d.Sync)
	b[111] = d.Seq
	b[112] = d.Options
	binary.BigEndian.PutUint16(b[113:], d.Universe)
	binary.BigEndian.PutUint16(b[115:], flagsLength(len(b)-115))
	b[117] = vectorDMPSet
	b[118] = 0xa1
	binary.BigEndian.PutUint16(b[121:], 1)
	binary.BigEndian.PutUint16(b[123:], uint16(1+len(slots)))
	b[125] = d.StartCode
	copy(b[126:], slots)
	return b
}

// Marshal builds one discovery page. At most 512 universes fit on a page.
func (d Discovery) Marshal() []byte {
	us := d.Universes
	if len(us) > 512 {
		us = us[:512]
	}
	b := make([]byte, 120+2*len(us))
	root(b, vectorRootExtended, d.CID)
	binary.BigEndian.PutUint16(b[38:], flagsLength(len(b)-38))
	binary.BigEndian.PutUint32(b[40:], vectorExtDiscovery)
	copy(b[44:107], d.Source)
	binary.BigEndian.PutUint16(b[112:], flagsLength(len(b)-112))
	binary.BigEndian.PutUint32(b[114:], vectorDiscoveryLst)
	b[118], b[119] = d.Page, d.Last
	for i, u := range us {
		binary.BigEndian.PutUint16(b[120+2*i:], u)
	}
	return b
}

// Parse reads a packet from the network. It returns a *Data or a *Discovery;
// anything else on the port (synchronisation, other ACN) is errNotSACN or an
// error saying what was wrong. The slots are a copy, so b can be reused.
func Parse(b []byte) (any, error) {
	if len(b) < 38 || binary.BigEndian.Uint16(b[0:]) != 0x0010 || string(b[4:16]) != string(acnID) {
		return nil, errNotSACN
	}
	var cid CID
	copy(cid[:], b[22:38])
	switch binary.BigEndian.Uint32(b[18:]) {
	case vectorRootData:
		if len(b) < dataHeader {
			return nil, fmt.Errorf("data packet of %d octets is too short", len(b))
		}
		if binary.BigEndian.Uint32(b[40:]) != vectorFramingData || b[117] != vectorDMPSet {
			return nil, errNotSACN
		}
		count := int(binary.BigEndian.Uint16(b[123:]))
		if count < 1 || count > 513 || dataHeader+count-1 > len(b) {
			return nil, fmt.Errorf("data packet says %d values but holds %d", count, len(b)-dataHeader+1)
		}
		return &Data{
			CID: cid, Source: name64(b[44:108]), Priority: b[108],
			Sync: binary.BigEndian.Uint16(b[109:]), Seq: b[111], Options: b[112],
			Universe: binary.BigEndian.Uint16(b[113:]), StartCode: b[125],
			Slots: append([]byte(nil), b[126:126+count-1]...),
		}, nil
	case vectorRootExtended:
		if len(b) < 120 || binary.BigEndian.Uint32(b[40:]) != vectorExtDiscovery || binary.BigEndian.Uint32(b[114:]) != vectorDiscoveryLst {
			return nil, errNotSACN // synchronisation packets land here
		}
		d := &Discovery{CID: cid, Source: name64(b[44:108]), Page: b[118], Last: b[119]}
		for i := 120; i+1 < len(b) && len(d.Universes) < 512; i += 2 {
			d.Universes = append(d.Universes, binary.BigEndian.Uint16(b[i:]))
		}
		return d, nil
	}
	return nil, errNotSACN
}
