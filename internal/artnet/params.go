package artnet

// The parameters a fixture describes itself with: SUPPORTED_PARAMETERS,
// PARAMETER_DESCRIPTION (manufacturer PIDs), sensors and the standard settings
// this tool offers. Shapes checked against a Creamsource Vortex 8S (fw 3.1.0)
// on 12 Nov 2026; its full answer is docs/samples/vortex8s-rdm-parameters.txt.

import (
	"encoding/binary"
	"fmt"
)

// More parameter IDs.
const (
	PIDParameterDescription uint16 = 0x0051
	PIDFactoryDefaults      uint16 = 0x0090
	PIDSensorDefinition     uint16 = 0x0200
	PIDSensorValue          uint16 = 0x0201
	PIDCurve                uint16 = 0x0343 // E1.37-1
	PIDCurveDescription     uint16 = 0x0344
	PIDDeviceHours          uint16 = 0x0400
	PIDLampHours            uint16 = 0x0401
	PIDDisplayInvert        uint16 = 0x0500
	PIDDisplayLevel         uint16 = 0x0501
	PIDPanInvert            uint16 = 0x0600
	PIDTiltInvert           uint16 = 0x0601
	PIDPanTiltSwap          uint16 = 0x0602
	PIDResetDevice          uint16 = 0x1001
)

func init() {
	for pid, name := range map[uint16]string{
		PIDParameterDescription: "PARAMETER_DESCRIPTION", PIDFactoryDefaults: "FACTORY_DEFAULTS", PIDSensorDefinition: "SENSOR_DEFINITION",
		PIDSensorValue: "SENSOR_VALUE", PIDCurve: "CURVE", PIDCurveDescription: "CURVE_DESCRIPTION", PIDDeviceHours: "DEVICE_HOURS",
		PIDLampHours: "LAMP_HOURS", PIDDisplayInvert: "DISPLAY_INVERT", PIDDisplayLevel: "DISPLAY_LEVEL", PIDPanInvert: "PAN_INVERT",
		PIDTiltInvert: "TILT_INVERT", PIDPanTiltSwap: "PAN_TILT_SWAP", PIDResetDevice: "RESET_DEVICE",
	} {
		pidNames[pid] = name
	}
}

// PIDList decodes SUPPORTED_PARAMETERS.
func PIDList(d []byte) []uint16 {
	out := make([]uint16, 0, len(d)/2)
	for i := 0; i+1 < len(d); i += 2 {
		out = append(out, binary.BigEndian.Uint16(d[i:]))
	}
	return out
}

// Data types of a PARAMETER_DESCRIPTION.
const (
	DataNotDefined byte = 0
	DataBitField   byte = 1
	DataASCII      byte = 2
	DataU8         byte = 3
	DataS8         byte = 4
	DataU16        byte = 5
	DataS16        byte = 6
	DataU32        byte = 7
	DataS32        byte = 8
)

// ParamDescription is a PARAMETER_DESCRIPTION reply.
type ParamDescription struct {
	PID         uint16
	Size        int // PDL
	DataType    byte
	CC          byte // 1 GET, 2 SET, 3 GET and SET
	Unit        byte
	Prefix      byte
	Min, Max    int64
	Default     int64
	Description string
}

func (p ParamDescription) CanGet() bool { return p.CC&1 != 0 }
func (p ParamDescription) CanSet() bool { return p.CC&2 != 0 }
func (p ParamDescription) Numeric() bool {
	return p.DataType >= DataU8 && p.DataType <= DataS32 || (p.DataType == DataNotDefined || p.DataType == DataBitField) && p.Size >= 1 && p.Size <= 4
}

// ParseParamDescription decodes PARAMETER_DESCRIPTION data. Min, max and default
// are 32-bit on the wire and read as signed or unsigned per the data type.
func ParseParamDescription(d []byte) (ParamDescription, error) {
	if len(d) < 20 {
		return ParamDescription{}, fmt.Errorf("PARAMETER_DESCRIPTION is %d bytes, expected at least 20", len(d))
	}
	be := binary.BigEndian
	p := ParamDescription{PID: be.Uint16(d), Size: int(d[2]), DataType: d[3], CC: d[4], Unit: d[6], Prefix: d[7], Description: Text(d[20:])}
	signed := p.DataType == DataS8 || p.DataType == DataS16 || p.DataType == DataS32
	num := func(b []byte) int64 {
		if signed {
			return int64(int32(be.Uint32(b)))
		}
		return int64(be.Uint32(b))
	}
	p.Min, p.Max, p.Default = num(d[8:]), num(d[12:]), num(d[16:])
	return p, nil
}

// Bytes encodes a PARAMETER_DESCRIPTION (used by the fake node).
func (p ParamDescription) Bytes() []byte {
	be := binary.BigEndian
	d := make([]byte, 20, 20+len(p.Description))
	be.PutUint16(d, p.PID)
	d[2], d[3], d[4], d[6], d[7] = byte(p.Size), p.DataType, p.CC, p.Unit, p.Prefix
	be.PutUint32(d[8:], uint32(p.Min))
	be.PutUint32(d[12:], uint32(p.Max))
	be.PutUint32(d[16:], uint32(p.Default))
	return append(d, p.Description...)
}

// DecodeNumber reads a numeric parameter value of n bytes, signed or not.
func DecodeNumber(d []byte, signed bool) (int64, error) {
	switch len(d) {
	case 1:
		if signed {
			return int64(int8(d[0])), nil
		}
		return int64(d[0]), nil
	case 2:
		if signed {
			return int64(int16(binary.BigEndian.Uint16(d))), nil
		}
		return int64(binary.BigEndian.Uint16(d)), nil
	case 4:
		if signed {
			return int64(int32(binary.BigEndian.Uint32(d))), nil
		}
		return int64(binary.BigEndian.Uint32(d)), nil
	}
	return 0, fmt.Errorf("a number of %d bytes", len(d))
}

// EncodeNumber writes v in n bytes, big-endian.
func EncodeNumber(v int64, n int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(v))
	return b[4-n:]
}

// SensorDefinition is a SENSOR_DEFINITION reply.
type SensorDefinition struct {
	Number               int
	Type, Unit, Prefix   byte
	RangeMin, RangeMax   int
	NormalMin, NormalMax int
	Recorded             byte
	Description          string
}

// ParseSensorDefinition decodes SENSOR_DEFINITION data.
func ParseSensorDefinition(d []byte) (SensorDefinition, error) {
	if len(d) < 13 {
		return SensorDefinition{}, fmt.Errorf("SENSOR_DEFINITION is %d bytes, expected at least 13", len(d))
	}
	s16 := func(b []byte) int { return int(int16(binary.BigEndian.Uint16(b))) }
	return SensorDefinition{Number: int(d[0]), Type: d[1], Unit: d[2], Prefix: d[3], RangeMin: s16(d[4:]), RangeMax: s16(d[6:]),
		NormalMin: s16(d[8:]), NormalMax: s16(d[10:]), Recorded: d[12], Description: Text(d[13:])}, nil
}

// Bytes encodes a SENSOR_DEFINITION (used by the fake node).
func (s SensorDefinition) Bytes() []byte {
	be := binary.BigEndian
	d := make([]byte, 13, 13+len(s.Description))
	d[0], d[1], d[2], d[3] = byte(s.Number), s.Type, s.Unit, s.Prefix
	be.PutUint16(d[4:], uint16(s.RangeMin))
	be.PutUint16(d[6:], uint16(s.RangeMax))
	be.PutUint16(d[8:], uint16(s.NormalMin))
	be.PutUint16(d[10:], uint16(s.NormalMax))
	d[12] = s.Recorded
	return append(d, s.Description...)
}

// SensorValue decodes SENSOR_VALUE data: the present reading.
func SensorValue(d []byte) (int, error) {
	if len(d) < 3 {
		return 0, fmt.Errorf("SENSOR_VALUE is %d bytes, expected 9", len(d))
	}
	return int(int16(binary.BigEndian.Uint16(d[1:]))), nil
}

var units = []string{"", "°C", "V DC", "V AC peak", "V AC", "A DC", "A AC peak", "A AC", "Hz", "Ω", "W", "kg", "m", "m²", "m³",
	"kg/m³", "m/s", "m/s²", "N", "J", "Pa", "s", "°", "sr", "cd", "lm", "lx", "IRE", "bytes"}

// UnitName is an E1.20 unit's symbol ("" for none or unknown).
func UnitName(u byte) string {
	if int(u) < len(units) {
		return units[u]
	}
	return ""
}

// The E1.20 prefixes as powers of ten (0x00 none, 0x01-0x08 deci..yocto, 0x11-0x18 deca..yotta).
func PrefixPower(p byte) int {
	switch {
	case p >= 0x01 && p <= 0x02:
		return -int(p)
	case p >= 0x03 && p <= 0x08:
		return -3 * int(p-2)
	case p >= 0x11 && p <= 0x12:
		return int(p - 0x10)
	case p >= 0x13 && p <= 0x18:
		return 3 * int(p-0x12)
	}
	return 0
}
