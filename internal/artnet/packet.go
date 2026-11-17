// Package artnet is an Art-Net 4 RDM controller: enough of the protocol to ask
// a node for the RDM devices on one of its DMX ports (ArtTodRequest / ArtTodData)
// and to send them RDM requests (ArtRdm), plus ArtPoll to see what a node
// offers. Everything is sent straight to the node's address on UDP 6454.
//
// Checked against a real LumiNode 12 (v2.9.1) on 12 Nov 2026: a DMX port fed
// by sACN universe N is reachable for RDM at Art-Net port-address N once its
// IO has rdm on, and ArtRdm carries the RDM message from the sub-start code to
// the checksum, without the 0xCC start code. The captures are in the tests.
package artnet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Port is the Art-Net UDP port. Every packet is sent to and from it.
const Port = 6454

const protVer = 14

// Art-Net opcodes used here.
const (
	OpPoll      uint16 = 0x2000
	OpPollReply uint16 = 0x2100
	OpTodReq    uint16 = 0x8000
	OpTodData   uint16 = 0x8100
	OpRdm       uint16 = 0x8300
)

var header = []byte("Art-Net\x00")

// PortAddress is Art-Net's 15-bit universe: Net (7 bits), Sub-Net (4), Universe (4).
type PortAddress uint16

func (p PortAddress) net() byte  { return byte(p >> 8 & 0x7F) }
func (p PortAddress) addr() byte { return byte(p) }

// String is the Net:Sub-Net:Universe form consoles show, with the number after it.
func (p PortAddress) String() string {
	return fmt.Sprintf("%d:%d:%d (%d)", p>>8&0x7F, p>>4&0xF, p&0xF, uint16(p))
}

func packet(op uint16, body []byte) []byte {
	b := make([]byte, 0, 10+len(body))
	b = append(b, header...)
	b = binary.LittleEndian.AppendUint16(b, op)
	return append(b, body...)
}

// Opcode returns the opcode of an Art-Net packet, or false when b is not one.
func Opcode(b []byte) (uint16, bool) {
	if len(b) < 10 || !bytes.Equal(b[:8], header) {
		return 0, false
	}
	return binary.LittleEndian.Uint16(b[8:10]), true
}

// PollPacket is an ArtPoll asking for replies to be sent to us.
func PollPacket() []byte { return packet(OpPoll, []byte{0, protVer, 0, 0}) }

// PollReply is the part of an ArtPollReply this tool uses. A node sends one per
// group of ports (BindIndex 1, 2, ...); a LumiNode sends one per port.
type PollReply struct {
	IP        net.IP
	BindIndex int
	ShortName string
	LongName  string
	RDM       bool // Status1 bit 1: the node can do RDM
	Ports     []PollPort
}

// PollPort is one port of an ArtPollReply.
type PollPort struct {
	Output  bool // the port can output DMX from the network
	Input   bool
	Address PortAddress // the output address (SwOut)
	Sending bool        // GoodOutputA bit 7: data is being output
	SACN    bool        // GoodOutputA bit 0: the output is fed by sACN
}

// ParsePollReply decodes an ArtPollReply.
func ParsePollReply(b []byte) (PollReply, error) {
	if op, ok := Opcode(b); !ok || op != OpPollReply {
		return PollReply{}, errors.New("not an ArtPollReply")
	}
	if len(b) < 207 {
		return PollReply{}, fmt.Errorf("ArtPollReply too short (%d bytes)", len(b))
	}
	r := PollReply{
		IP:        net.IPv4(b[10], b[11], b[12], b[13]),
		ShortName: cstr(b[26:44]),
		LongName:  cstr(b[44:108]),
		RDM:       b[23]&0x02 != 0,
		BindIndex: 1,
	}
	if len(b) > 211 && b[211] != 0 {
		r.BindIndex = int(b[211])
	}
	n := int(b[173])
	if n > 4 {
		n = 4
	}
	for i := 0; i < n; i++ {
		t := b[174+i]
		r.Ports = append(r.Ports, PollPort{
			Output:  t&0x80 != 0,
			Input:   t&0x40 != 0,
			Address: PortAddress(uint16(b[18]&0x7F)<<8 | uint16(b[19]&0x0F)<<4 | uint16(b[190+i]&0x0F)),
			Sending: b[182+i]&0x80 != 0,
			SACN:    b[182+i]&0x01 != 0,
		})
	}
	return r, nil
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

// TodRequestPacket asks for the full table of devices on one port-address.
func TodRequestPacket(pa PortAddress) []byte {
	body := make([]byte, 46)
	body[1] = protVer
	body[11] = pa.net() // after ProtVer (2), Filler1-2 (2), Spare1-7 (7)
	body[12] = 0        // TodFull
	body[13] = 1        // one address
	body[14] = pa.addr()
	return packet(OpTodReq, body)
}

// TodData is one ArtTodData packet: a block of the device table for a port.
type TodData struct {
	Address    PortAddress
	BindIndex  int
	Port       int
	Nak        bool // CommandResponse 0xFF: the node could not send the table
	UIDTotal   int  // devices on the port, across every block
	BlockCount int  // which block this is, from 0
	UIDs       []UID
}

// ParseTodData decodes an ArtTodData.
func ParseTodData(b []byte) (TodData, error) {
	if op, ok := Opcode(b); !ok || op != OpTodData {
		return TodData{}, errors.New("not an ArtTodData")
	}
	if len(b) < 28 {
		return TodData{}, fmt.Errorf("ArtTodData too short (%d bytes)", len(b))
	}
	t := TodData{
		Port:       int(b[13]),
		BindIndex:  int(b[20]),
		Address:    PortAddress(uint16(b[21]&0x7F)<<8 | uint16(b[23])),
		Nak:        b[22] == 0xFF,
		UIDTotal:   int(binary.BigEndian.Uint16(b[24:26])),
		BlockCount: int(b[26]),
	}
	n := int(b[27])
	if len(b) < 28+6*n {
		return TodData{}, fmt.Errorf("ArtTodData says %d UIDs but has room for %d", n, (len(b)-28)/6)
	}
	for i := 0; i < n; i++ {
		t.UIDs = append(t.UIDs, UIDFromBytes(b[28+6*i:]))
	}
	return t, nil
}

// TodDataPacket builds an ArtTodData (used by the fake node).
func TodDataPacket(pa PortAddress, bindIndex, total, block int, uids []UID) []byte {
	body := make([]byte, 18, 18+6*len(uids))
	body[1] = protVer
	body[2] = 1 // RDM standard version
	body[3] = 1 // port
	body[10] = byte(bindIndex)
	body[11] = pa.net()
	body[12] = 0 // TodFull
	body[13] = pa.addr()
	binary.BigEndian.PutUint16(body[14:16], uint16(total))
	body[16] = byte(block)
	body[17] = byte(len(uids))
	for _, u := range uids {
		body = append(body, u.Bytes()...)
	}
	return packet(OpTodData, body)
}

// RdmPacket wraps an RDM message (from the sub-start code, with the checksum)
// in an ArtRdm for a port-address.
func RdmPacket(pa PortAddress, msg []byte) []byte {
	body := make([]byte, 14, 14+len(msg))
	body[1] = protVer
	body[2] = 1 // RDM standard version
	body[11] = pa.net()
	body[12] = 0 // ArProcess
	body[13] = pa.addr()
	return packet(OpRdm, append(body, msg...))
}

// ParseRdmPacket returns the port-address and RDM message of an ArtRdm.
func ParseRdmPacket(b []byte) (PortAddress, []byte, error) {
	if op, ok := Opcode(b); !ok || op != OpRdm {
		return 0, nil, errors.New("not an ArtRdm")
	}
	if len(b) < 24+rdmHeaderLen {
		return 0, nil, fmt.Errorf("ArtRdm too short (%d bytes)", len(b))
	}
	return PortAddress(uint16(b[21]&0x7F)<<8 | uint16(b[23])), b[24:], nil
}
