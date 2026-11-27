package artnet

// A stand-in for a node's Art-Net RDM side, for the tests and cmd/fakenode:
// it answers ArtTodRequest and RDM GETs for fake fixtures, the way the real
// LumiNode 12 did on 12 Nov 2026.

import (
	"encoding/binary"
	"net"
	"sync"
	"time"
)

// FakeFixture is one fake RDM device.
type FakeFixture struct {
	UID           UID
	Manufacturer  string
	Model         string
	ModelID       uint16
	Category      uint16
	Label         string
	Software      string
	SoftwareID    uint32
	Start         uint16
	Personality   uint8 // 1-based
	Personalities []FakePersonality
	Sensors       uint8
	Identify      bool
	ReadOnlyLabel bool // NACK a label SET with "write protected"
	// What the fixture page reads: manufacturer parameters, sensors, hours, dimmer
	// curves (E1.37-1), whether it is at factory defaults, and restarts received.
	Params       []*FakeParam
	SensorDefs   []FakeSensor // when set, DEVICE_INFO counts these
	Hours        uint32
	LampHours    uint32
	Curves       []string
	Curve        uint8
	AtDefaults   bool
	Restarts     int
	AckTimerOnce bool // answer the first DEVICE_LABEL with ACK_TIMER, as a slow device would
	Silent       bool // never answer RDM (a device that has gone)
	MuteReset    bool // do a factory reset but never acknowledge it
	LateReset    bool // acknowledge a factory reset at once and apply it a moment later
	// What each channel is, for SLOT_INFO, and its name for SLOT_DESCRIPTION.
	// A fixture with no Slots does not know either request, like many real ones.
	Slots     []Slot
	SlotNames map[uint16]string

	timerSent bool
	resetAt   time.Time // LateReset: acknowledged, applied from this moment
}

func (fx *FakeFixture) toDefaults() {
	fx.Start, fx.Personality, fx.Label, fx.Identify = 1, 1, "", false
	if len(fx.Curves) > 0 {
		fx.Curve = 1
	}
	for _, p := range fx.Params {
		switch {
		case p.Factory != nil:
			p.Value = append([]byte(nil), p.Factory...)
		case p.Desc.CanGet() && p.Desc.Numeric():
			p.Value = EncodeNumber(p.Desc.Default, p.Desc.Size)
		}
	}
	fx.AtDefaults = true
}

// FakeParam is a manufacturer parameter of a fake fixture.
type FakeParam struct {
	Desc    ParamDescription
	Value   []byte
	Factory []byte // what a factory reset sets; nil means Desc.Default. The real Vortex's differ from its descriptions.
}

// FakeSensor is a sensor of a fake fixture.
type FakeSensor struct {
	Def   SensorDefinition
	Value int
}

// FakePersonality is one DMX personality of a fake fixture.
type FakePersonality struct {
	Footprint uint16
	Name      string
}

// FakeNode answers Art-Net RDM for a set of port-addresses.
type FakeNode struct {
	conn *net.UDPConn
	mu   sync.Mutex
	// Ports maps a port-address to its fixtures. A port-address where RDM is
	// off (see ServeFake) ignores ArtTodRequest and ArtRdm, like a real node.
	Ports map[PortAddress][]*FakeFixture
	// SharedPorts are more DMX ports fed from the same port-address, as on a
	// LumiNode with two ports on one sACN universe: each answers ArtTodRequest
	// with its own table, BindIndex 2, 3, ..., sent before port 1's.
	SharedPorts map[PortAddress][][]*FakeFixture
	// TodBinds overrides the BindIndex of each table at a port-address, in the
	// order they are sent (shared ports, then the port's own), as a real node's
	// do not follow the port number.
	TodBinds  map[PortAddress][]int
	enabledFn func(PortAddress) bool
	listedFn  func(PortAddress) map[UID]bool
	// Requests counts the RDM requests received.
	Requests int
}

// ServeFake listens on addr ("127.0.0.1:0" in tests) and answers until Close.
// enabled says whether RDM is on at a port-address (nil: always). It is called
// without the fake's lock held, so it may take the caller's own locks.
func ServeFake(addr string, ports map[PortAddress][]*FakeFixture, enabled func(PortAddress) bool) (*FakeNode, error) {
	a, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", a)
	if err != nil {
		return nil, err
	}
	f := &FakeNode{conn: conn, Ports: ports, enabledFn: enabled}
	go f.serve()
	return f, nil
}

// Addr is where the fake listens.
func (f *FakeNode) Addr() *net.UDPAddr { return f.conn.LocalAddr().(*net.UDPAddr) }

// Close stops the fake.
func (f *FakeNode) Close() error { return f.conn.Close() }

// Lock and Unlock guard Ports and the fixtures while a caller changes them.
func (f *FakeNode) Lock()   { f.mu.Lock() }
func (f *FakeNode) Unlock() { f.mu.Unlock() }

func (f *FakeNode) enabled(pa PortAddress) bool { return f.enabledFn == nil || f.enabledFn(pa) }

// SetListed limits what ArtTodRequest lists at a port-address to the fixtures
// the node has found so far (a nil map: all of them). Like enabled, fn is called
// without the fake's lock held.
func (f *FakeNode) SetListed(fn func(PortAddress) map[UID]bool) {
	f.mu.Lock()
	f.listedFn = fn
	f.mu.Unlock()
}

func (f *FakeNode) serve() {
	buf := make([]byte, 2048)
	for {
		n, from, err := f.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		b := buf[:n]
		op, ok := Opcode(b)
		if !ok {
			continue
		}
		// Whether RDM is on is asked before taking the lock: the callback takes
		// the caller's locks, and the caller takes ours while holding them.
		var pa PortAddress
		var m []byte
		var listed map[UID]bool
		on := false
		switch op {
		case OpTodReq:
			if len(b) >= 25 {
				pa = PortAddress(uint16(b[21]&0x7F)<<8 | uint16(b[24]))
				on = f.enabled(pa)
				f.mu.Lock()
				fn := f.listedFn
				f.mu.Unlock()
				if on && fn != nil {
					listed = fn(pa)
				}
			}
		case OpRdm:
			var err error
			if pa, m, err = ParseRdmPacket(b); err == nil {
				on = f.enabled(pa)
			}
		}
		if !on {
			continue
		}
		f.mu.Lock()
		var replies [][]byte
		switch op {
		case OpTodReq:
			sent := 0
			table := func(bind int, fx []*FakeFixture) {
				if b := f.TodBinds[pa]; sent < len(b) {
					bind = b[sent]
				}
				sent++
				var uids []UID
				for _, x := range fx {
					if listed == nil || listed[x.UID] {
						uids = append(uids, x.UID)
					}
				}
				for block := 0; block == 0 || block*200 < len(uids); block++ {
					part := uids[block*200 : min(len(uids), block*200+200)]
					replies = append(replies, TodDataPacket(pa, bind, len(uids), block, part))
				}
			}
			for i, fx := range f.SharedPorts[pa] {
				table(i+2, fx)
			}
			if fx, ok := f.Ports[pa]; ok {
				table(1, fx)
			}
		case OpRdm:
			f.Requests++
			if r := f.answer(pa, m); r != nil {
				replies = append(replies, RdmPacket(pa, r))
			}
		}
		f.mu.Unlock()
		for _, r := range replies {
			_, _ = f.conn.WriteToUDP(r, from)
		}
	}
}

func (f *FakeNode) answer(pa PortAddress, m []byte) []byte {
	if len(m) < rdmHeaderLen {
		return nil
	}
	dest, src, tn, cc := UIDFromBytes(m[2:]), UIDFromBytes(m[8:]), m[14], m[19]
	pid := binary.BigEndian.Uint16(m[20:22])
	data := m[rdmHeaderLen : rdmHeaderLen+int(m[22])]
	var fx *FakeFixture
	for _, x := range f.Ports[pa] {
		if x.UID == dest {
			fx = x
		}
	}
	for _, line := range f.SharedPorts[pa] { // a real node sends it out on every line at the address
		for _, x := range line {
			if x.UID == dest {
				fx = x
			}
		}
	}
	if fx == nil || fx.Silent || (cc != CCGet && cc != CCSet) {
		return nil
	}
	if !fx.resetAt.IsZero() && time.Now().After(fx.resetAt) {
		fx.resetAt = time.Time{}
		fx.toDefaults()
	}
	resp := Response{Dest: src, Src: fx.UID, TN: tn, Type: RespAck, CC: CCGetResponse, PID: pid}
	nack := func(reason uint16) []byte {
		resp.Type, resp.Data = RespNack, binary.BigEndian.AppendUint16(nil, reason)
		return EncodeResponse(resp)
	}
	pers := func(n uint8) (FakePersonality, bool) {
		if n < 1 || int(n) > len(fx.Personalities) {
			return FakePersonality{}, false
		}
		return fx.Personalities[n-1], true
	}
	if r, ok := fx.answerParams(resp, cc, pid, data); ok {
		return r
	}
	if cc == CCSet {
		fx.AtDefaults = false
		resp.CC = CCSetResponse
		be := binary.BigEndian
		switch {
		case pid == PIDIdentifyDevice && len(data) == 1 && data[0] <= 1:
			fx.Identify = data[0] == 1
		case pid == PIDDMXStartAddress && len(data) == 2:
			a := be.Uint16(data)
			if a < 1 || a > 512 {
				return nack(6)
			}
			fx.Start = a
		case pid == PIDDeviceLabel && len(data) <= 32:
			if fx.ReadOnlyLabel {
				return nack(4)
			}
			fx.Label = string(data)
		case pid == PIDDMXPersonality && len(data) == 1:
			if _, ok := pers(data[0]); !ok {
				return nack(6)
			}
			fx.Personality = data[0]
		case pid == PIDIdentifyDevice || pid == PIDDMXStartAddress || pid == PIDDeviceLabel || pid == PIDDMXPersonality:
			return nack(1)
		default:
			return nack(5)
		}
		return EncodeResponse(resp)
	}
	switch pid {
	case PIDIdentifyDevice:
		resp.Data = []byte{0}
		if fx.Identify {
			resp.Data[0] = 1
		}
	case PIDQueuedMessage: // the reply held back by ACK_TIMER
		resp.PID, resp.Data = PIDDeviceLabel, []byte(fx.Label)
	case PIDDeviceInfo:
		p, _ := pers(fx.Personality)
		sensors := fx.Sensors
		if len(fx.SensorDefs) > 0 {
			sensors = uint8(len(fx.SensorDefs))
		}
		start := fx.Start
		if p.Footprint == 0 {
			start = 0xFFFF
		}
		resp.Data = DeviceInfo{ProtocolVersion: 0x0100, Model: fx.ModelID, Category: fx.Category, SoftwareVersion: fx.SoftwareID,
			Footprint: p.Footprint, Personality: fx.Personality, PersonalityCount: uint8(len(fx.Personalities)), StartAddress: start,
			Sensors: sensors}.Bytes()
	case PIDDeviceModelDescription:
		resp.Data = []byte(fx.Model)
	case PIDManufacturerLabel:
		resp.Data = []byte(fx.Manufacturer)
	case PIDDeviceLabel:
		if fx.AckTimerOnce && !fx.timerSent {
			fx.timerSent = true
			resp.Type, resp.Data = RespAckTimer, []byte{0, 1}
			break
		}
		resp.Data = []byte(fx.Label)
	case PIDSoftwareVersionLabel:
		resp.Data = []byte(fx.Software)
	case PIDDMXStartAddress:
		resp.Data = binary.BigEndian.AppendUint16(nil, fx.Start)
	case PIDDMXPersonality:
		resp.Data = []byte{fx.Personality, uint8(len(fx.Personalities))}
	case PIDSlotInfo:
		if len(fx.Slots) == 0 {
			return nack(0)
		}
		resp.Data = SlotInfoBytes(fx.Slots)
	case PIDSlotDescription:
		if len(data) < 2 || len(fx.Slots) == 0 {
			return nack(0)
		}
		n := binary.BigEndian.Uint16(data)
		name, ok := fx.SlotNames[n]
		if !ok {
			return nack(6)
		}
		resp.Data = append(binary.BigEndian.AppendUint16(nil, n), name...)
	case PIDDMXPersonalityDescription:
		if len(data) < 1 {
			return nack(1)
		}
		p, ok := pers(data[0])
		if !ok {
			return nack(6)
		}
		resp.Data = append(binary.BigEndian.AppendUint16([]byte{data[0]}, p.Footprint), p.Name...)
	default:
		return nack(0)
	}
	return EncodeResponse(resp)
}

// answerParams handles the fixture page's parameters, GET and SET. ok is false
// for a PID it does not handle.
func (fx *FakeFixture) answerParams(resp Response, cc byte, pid uint16, data []byte) (reply []byte, ok bool) {
	be := binary.BigEndian
	if cc == CCSet {
		resp.CC = CCSetResponse
	}
	nack := func(reason uint16) ([]byte, bool) {
		resp.Type, resp.Data = RespNack, be.AppendUint16(nil, reason)
		return EncodeResponse(resp), true
	}
	ack := func(d []byte) ([]byte, bool) { resp.Data = d; return EncodeResponse(resp), true }
	for _, p := range fx.Params {
		if p.Desc.PID != pid {
			continue
		}
		if cc == CCGet {
			if !p.Desc.CanGet() {
				return nack(5)
			}
			return ack(p.Value)
		}
		if !p.Desc.CanSet() {
			return nack(5)
		}
		if p.Desc.Numeric() {
			if len(data) != p.Desc.Size {
				return nack(1)
			}
			v, _ := DecodeNumber(data, p.Desc.DataType == DataS8 || p.Desc.DataType == DataS16 || p.Desc.DataType == DataS32)
			if v < p.Desc.Min || v > p.Desc.Max {
				return nack(6)
			}
		} else if len(data) > p.Desc.Size {
			return nack(1)
		}
		if p.Desc.CanGet() { // a SET-only parameter is an action: nothing to keep
			p.Value = append([]byte(nil), data...)
		}
		fx.AtDefaults = false
		return ack(nil)
	}
	switch {
	case pid == PIDSupportedParameters && cc == CCGet:
		var d []byte
		std := []uint16{PIDDeviceModelDescription, PIDManufacturerLabel, PIDDeviceLabel, PIDDMXPersonality, PIDDMXPersonalityDescription, PIDFactoryDefaults, PIDResetDevice}
		if len(fx.SensorDefs) > 0 {
			std = append(std, PIDSensorDefinition, PIDSensorValue)
		}
		if fx.Hours > 0 {
			std = append(std, PIDDeviceHours, PIDLampHours)
		}
		if len(fx.Curves) > 0 {
			std = append(std, PIDCurve, PIDCurveDescription)
		}
		for _, p := range std {
			d = be.AppendUint16(d, p)
		}
		for _, p := range fx.Params {
			d = be.AppendUint16(d, p.Desc.PID)
		}
		return ack(d)
	case pid == PIDParameterDescription && cc == CCGet:
		if len(data) != 2 {
			return nack(1)
		}
		for _, p := range fx.Params {
			if p.Desc.PID == be.Uint16(data) {
				return ack(p.Desc.Bytes())
			}
		}
		return nack(6)
	case pid == PIDSensorDefinition && cc == CCGet || pid == PIDSensorValue && cc == CCGet:
		if len(data) != 1 || int(data[0]) >= len(fx.SensorDefs) {
			return nack(6)
		}
		sn := fx.SensorDefs[data[0]]
		if pid == PIDSensorDefinition {
			return ack(sn.Def.Bytes())
		}
		v := be.AppendUint16([]byte{data[0]}, uint16(sn.Value))
		return ack(append(v, 0, 0, 0, 0, 0, 0))
	case pid == PIDDeviceHours && cc == CCGet:
		return ack(be.AppendUint32(nil, fx.Hours))
	case pid == PIDLampHours && cc == CCGet:
		return ack(be.AppendUint32(nil, fx.LampHours))
	case pid == PIDCurve && len(fx.Curves) > 0:
		if cc == CCGet {
			return ack([]byte{fx.Curve, byte(len(fx.Curves))})
		}
		if len(data) != 1 || data[0] < 1 || int(data[0]) > len(fx.Curves) {
			return nack(6)
		}
		fx.Curve, fx.AtDefaults = data[0], false
		return ack(nil)
	case pid == PIDCurveDescription && cc == CCGet && len(fx.Curves) > 0:
		if len(data) != 1 || data[0] < 1 || int(data[0]) > len(fx.Curves) {
			return nack(6)
		}
		return ack(append([]byte{data[0]}, fx.Curves[data[0]-1]...))
	case pid == PIDFactoryDefaults:
		if cc == CCGet {
			if fx.AtDefaults {
				return ack([]byte{1})
			}
			return ack([]byte{0})
		}
		if fx.LateReset {
			fx.resetAt = time.Now().Add(150 * time.Millisecond)
			return ack(nil)
		}
		fx.toDefaults()
		if fx.MuteReset {
			return nil, true
		}
		return ack(nil)
	case pid == PIDResetDevice && cc == CCSet:
		if len(data) != 1 || data[0] != 0x01 && data[0] != 0xFF {
			return nack(6)
		}
		fx.Restarts++
		return ack(nil)
	}
	return nil, false
}

// WithVortexParams gives a fake fixture the parameters, sensors, hours and
// curves a real Creamsource Vortex 8S (fw 3.1.0) reported on 12 Nov 2026 (see
// docs/samples/vortex8s-rdm-parameters.txt), with its curve names. A factory
// reset puts back the values it had then, as the real one did: its factory
// settings are not the defaults its PARAMETER_DESCRIPTIONs give (CRMX Enabled
// says default 0 but comes back on), and its label comes back as model and serial.
func (fx *FakeFixture) WithVortexParams() *FakeFixture {
	u8 := func(pid uint16, name string, min, max, def int64, cc byte, v byte) *FakeParam {
		p := &FakeParam{Desc: ParamDescription{PID: pid, Size: 1, DataType: DataU8, CC: cc, Min: min, Max: max, Default: def, Description: name}}
		if cc&1 != 0 {
			p.Value = []byte{v}
		}
		return p
	}
	text := func(pid uint16, name string, size int, cc byte, v string) *FakeParam {
		return &FakeParam{Desc: ParamDescription{PID: pid, Size: size, DataType: DataASCII, CC: cc, Description: name}, Value: []byte(v)}
	}
	fx.Params = []*FakeParam{
		u8(0x8500, "DMX Smoothing", 1, 3, 1, 3, 1), text(0x8501, "DMX Smoothing Name", 32, 1, "Off"),
		u8(0x8502, "DMX Forwarding", 0, 1, 0, 3, 0), u8(0x8505, "Restore DMX Preset", 1, 4, 1, 2, 0),
		u8(0x8510, "High Speed", 0, 1, 0, 3, 0), u8(0x8514, "RGBW Colourspace", 0, 7, 0, 3, 1),
		text(0x8515, "RGBW Colourspace Name", 32, 1, "Calibrated RGBW"), u8(0x8512, "Blackout on Startup", 0, 1, 0, 3, 0),
		u8(0x8800, "Master Mode", 0, 1, 0, 3, 1), u8(0x8600, "CRMX Enabled", 0, 1, 0, 3, 1), u8(0x8601, "CRMX Link State", 0, 1, 0, 1, 0),
		u8(0x8602, "CRMX BLE Enabled", 0, 1, 0, 3, 1), u8(0x8700, "SACN Enabled", 0, 1, 0, 3, 1),
		{Desc: ParamDescription{PID: 0x8701, Size: 2, DataType: DataU16, CC: 3, Min: 1, Max: 63999, Default: 1, Description: "SACN Universe"}, Value: []byte{0, 1}},
		u8(0x8513, "Display Always On", 0, 1, 0, 3, 1), text(0x8400, "Device Location", 20, 3, ""),
	}
	fx.SensorDefs = []FakeSensor{{Def: SensorDefinition{Number: 0, Type: 0, Unit: 1, RangeMin: -40, RangeMax: 120, NormalMin: -20, NormalMax: 80, Description: "LED Panel Temperature"}, Value: 14}}
	for i := 1; i <= 4; i++ {
		fx.SensorDefs = append(fx.SensorDefs, FakeSensor{Def: SensorDefinition{Number: i, Type: 11, RangeMax: 3000, NormalMax: 3000, Description: "Fan " + string(rune('0'+i)) + " RPM"}})
	}
	fx.Hours, fx.LampHours = 1605, 887
	for _, p := range fx.Params {
		if p.Value != nil {
			p.Factory = append([]byte{}, p.Value...) // non-nil even for an empty text
		}
	}
	fx.Curves, fx.Curve = []string{"Linear", "Exponential", "Logarithmic", "S-Curve"}, 1
	return fx
}
