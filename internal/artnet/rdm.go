package artnet

// RDM (ANSI E1.20) messages as ArtRdm carries them: from the sub-start code to
// the checksum, without the 0xCC start code (the checksum still counts it).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// UID is an RDM device's 48-bit unique ID: manufacturer (16 bits), device (32).
type UID uint64

// UIDFromBytes reads a UID from the first six bytes of b.
func UIDFromBytes(b []byte) UID {
	return UID(uint64(binary.BigEndian.Uint16(b))<<32 | uint64(binary.BigEndian.Uint32(b[2:])))
}

// Bytes is the UID on the wire.
func (u UID) Bytes() []byte {
	b := make([]byte, 6)
	binary.BigEndian.PutUint16(b, u.Manufacturer())
	binary.BigEndian.PutUint32(b[2:], uint32(u))
	return b
}

// Manufacturer is the ESTA manufacturer ID (0x4C4C is Luminex).
func (u UID) Manufacturer() uint16 { return uint16(u >> 32) }

// Broadcast is true for the all-devices UID FFFF:FFFFFFFF and for a
// manufacturer's broadcast MMMM:FFFFFFFF: every device it covers acts on a SET
// and none replies. This tool only ever talks to one device at a time.
func (u UID) Broadcast() bool { return uint32(u) == 0xFFFFFFFF }

// String is the usual MMMM:DDDDDDDD form.
func (u UID) String() string { return fmt.Sprintf("%04X:%08X", u.Manufacturer(), uint32(u)) }

// ParseUID reads MMMM:DDDDDDDD, a LumiNode's 4F:75:00:0D:C1:13, or 12 hex digits.
func ParseUID(s string) (UID, error) {
	h := strings.NewReplacer(":", "", "-", "", " ", "").Replace(s)
	if len(h) != 12 {
		return 0, fmt.Errorf("bad RDM UID %q", s)
	}
	v, err := strconv.ParseUint(h, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("bad RDM UID %q", s)
	}
	return UID(v), nil
}

// Command classes.
const (
	CCGet         byte = 0x20
	CCGetResponse byte = 0x21
	CCSet         byte = 0x30
	CCSetResponse byte = 0x31
)

// Response types.
const (
	RespAck         byte = 0
	RespAckTimer    byte = 1
	RespNack        byte = 2
	RespAckOverflow byte = 3
)

// Parameter IDs read by this tool.
const (
	PIDQueuedMessage             uint16 = 0x0020
	PIDSupportedParameters       uint16 = 0x0050
	PIDDeviceInfo                uint16 = 0x0060
	PIDDeviceModelDescription    uint16 = 0x0080
	PIDManufacturerLabel         uint16 = 0x0081
	PIDDeviceLabel               uint16 = 0x0082
	PIDSoftwareVersionLabel      uint16 = 0x00C0
	PIDDMXPersonality            uint16 = 0x00E0
	PIDDMXPersonalityDescription uint16 = 0x00E1
	PIDDMXStartAddress           uint16 = 0x00F0
	PIDIdentifyDevice            uint16 = 0x1000
)

var pidNames = map[uint16]string{
	PIDQueuedMessage: "QUEUED_MESSAGE", PIDSupportedParameters: "SUPPORTED_PARAMETERS", PIDDeviceInfo: "DEVICE_INFO",
	PIDDeviceModelDescription: "DEVICE_MODEL_DESCRIPTION", PIDManufacturerLabel: "MANUFACTURER_LABEL", PIDDeviceLabel: "DEVICE_LABEL",
	PIDSoftwareVersionLabel: "SOFTWARE_VERSION_LABEL", PIDDMXPersonality: "DMX_PERSONALITY",
	PIDDMXPersonalityDescription: "DMX_PERSONALITY_DESCRIPTION", PIDDMXStartAddress: "DMX_START_ADDRESS", PIDIdentifyDevice: "IDENTIFY_DEVICE",
}

// PIDName is the E1.20 name of a parameter, or its number.
func PIDName(pid uint16) string {
	if n, ok := pidNames[pid]; ok {
		return n
	}
	return fmt.Sprintf("PID 0x%04X", pid)
}

var nackReasons = []string{"unknown PID", "format error", "hardware fault", "proxy reject", "write protected",
	"unsupported command class", "data out of range", "buffer full", "packet size unsupported", "sub-device out of range", "proxy buffer full"}

// NackError is a device's refusal of a request.
type NackError struct {
	PID    uint16
	Reason uint16
}

func (e *NackError) Error() string {
	r := fmt.Sprintf("reason 0x%04X", e.Reason)
	if int(e.Reason) < len(nackReasons) {
		r = nackReasons[e.Reason]
	}
	return fmt.Sprintf("%s refused: %s", PIDName(e.PID), r)
}

// Request is one RDM request.
type Request struct {
	Dest      UID
	Src       UID
	TN        byte
	SubDevice uint16
	CC        byte
	PID       uint16
	Data      []byte
}

const rdmHeaderLen = 23 // sub-start code to PDL, without the start code

// Encode is the request from the sub-start code to the checksum.
func (r Request) Encode() []byte {
	return encodeMsg(r.Dest, r.Src, r.TN, 1, r.SubDevice, r.CC, r.PID, r.Data)
}

func encodeMsg(dest, src UID, tn, portOrType byte, sub uint16, cc byte, pid uint16, data []byte) []byte {
	m := make([]byte, 0, rdmHeaderLen+len(data)+2)
	m = append(m, 0x01, byte(24+len(data)))
	m = append(m, dest.Bytes()...)
	m = append(m, src.Bytes()...)
	m = append(m, tn, portOrType, 0)
	m = binary.BigEndian.AppendUint16(m, sub)
	m = append(m, cc)
	m = binary.BigEndian.AppendUint16(m, pid)
	m = append(m, byte(len(data)))
	m = append(m, data...)
	return binary.BigEndian.AppendUint16(m, checksum(m))
}

func checksum(m []byte) uint16 {
	sum := uint16(0xCC)
	for _, c := range m {
		sum += uint16(c)
	}
	return sum
}

// Response is one RDM response.
type Response struct {
	Dest     UID // the controller
	Src      UID // the device
	TN       byte
	Type     byte
	MsgCount byte
	CC       byte
	PID      uint16
	Data     []byte
}

// ParseResponse decodes an RDM message and checks its length and checksum.
func ParseResponse(m []byte) (Response, error) {
	if len(m) < rdmHeaderLen || m[0] != 0x01 {
		return Response{}, errors.New("not an RDM message")
	}
	pdl := int(m[22])
	if int(m[1]) != 24+pdl || len(m) < rdmHeaderLen+pdl {
		return Response{}, fmt.Errorf("RDM length mismatch (length %d, PDL %d, %d bytes)", m[1], pdl, len(m))
	}
	end := rdmHeaderLen + pdl
	if len(m) >= end+2 { // a checksum is there: it must match
		if got, want := binary.BigEndian.Uint16(m[end:]), checksum(m[:end]); got != want {
			return Response{}, fmt.Errorf("RDM checksum 0x%04X, expected 0x%04X", got, want)
		}
	}
	return Response{
		Dest: UIDFromBytes(m[2:]), Src: UIDFromBytes(m[8:]), TN: m[14], Type: m[15], MsgCount: m[16],
		CC: m[19], PID: binary.BigEndian.Uint16(m[20:22]), Data: append([]byte(nil), m[23:end]...),
	}, nil
}

// EncodeResponse builds a response message (used by the fake node).
func EncodeResponse(r Response) []byte {
	return encodeMsg(r.Dest, r.Src, r.TN, r.Type, 0, r.CC, r.PID, r.Data)
}

// DeviceInfo is the DEVICE_INFO reply.
type DeviceInfo struct {
	ProtocolVersion  uint16
	Model            uint16
	Category         uint16
	SoftwareVersion  uint32
	Footprint        uint16
	Personality      uint8
	PersonalityCount uint8
	StartAddress     uint16 // 0xFFFF when the device has no DMX footprint
	SubDevices       uint16
	Sensors          uint8
}

// ParseDeviceInfo decodes DEVICE_INFO parameter data.
func ParseDeviceInfo(d []byte) (DeviceInfo, error) {
	if len(d) < 19 {
		return DeviceInfo{}, fmt.Errorf("DEVICE_INFO is %d bytes, expected 19", len(d))
	}
	be := binary.BigEndian
	return DeviceInfo{
		ProtocolVersion: be.Uint16(d), Model: be.Uint16(d[2:]), Category: be.Uint16(d[4:]), SoftwareVersion: be.Uint32(d[6:]),
		Footprint: be.Uint16(d[10:]), Personality: d[12], PersonalityCount: d[13], StartAddress: be.Uint16(d[14:]),
		SubDevices: be.Uint16(d[16:]), Sensors: d[18],
	}, nil
}

// Bytes encodes DEVICE_INFO (used by the fake node).
func (i DeviceInfo) Bytes() []byte {
	be := binary.BigEndian
	d := make([]byte, 19)
	be.PutUint16(d, i.ProtocolVersion)
	be.PutUint16(d[2:], i.Model)
	be.PutUint16(d[4:], i.Category)
	be.PutUint32(d[6:], i.SoftwareVersion)
	be.PutUint16(d[10:], i.Footprint)
	d[12], d[13] = i.Personality, i.PersonalityCount
	be.PutUint16(d[14:], i.StartAddress)
	be.PutUint16(d[16:], i.SubDevices)
	d[18] = i.Sensors
	return d
}

// Text decodes a label parameter: ASCII, up to 32 characters, sometimes NUL padded.
func Text(d []byte) string { return cstr(d) }

var categories = map[byte]string{
	0x01: "Fixture", 0x02: "Fixture accessory", 0x03: "Projector", 0x04: "Atmospheric", 0x05: "Dimmer", 0x06: "Power",
	0x07: "Scenic", 0x08: "Data", 0x09: "AV", 0x0A: "Monitor", 0x70: "Control", 0x71: "Test", 0x7F: "Other",
}

// CategoryName is the broad product category (E1.20 table A-5, by its high byte).
func CategoryName(c uint16) string {
	if n, ok := categories[byte(c>>8)]; ok {
		return n
	}
	if c == 0 {
		return ""
	}
	return fmt.Sprintf("0x%04X", c)
}
