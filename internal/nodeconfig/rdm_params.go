package nodeconfig

// The fixture page: everything a fixture says it supports, read with
// SUPPORTED_PARAMETERS and PARAMETER_DESCRIPTION, so one page works for any
// fixture. A Creamsource Vortex 8S (fw 3.1.0) answered with sixteen of its own
// parameters (CRMX Enabled, SACN Universe, DMX Smoothing...), the E1.37-1 dimmer
// curve, hours, five sensors, FACTORY_DEFAULTS and RESET_DEVICE; see
// docs/samples/vortex8s-rdm-parameters.txt.

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/technickirl/lighting-network-tools/internal/artnet"
	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

// rdmSetting is one setting on the fixture page.
type rdmSetting struct {
	PID      int      `json:"pid"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"` // bool, number, choice, text, action (SET only)
	ReadOnly bool     `json:"readOnly"`
	Min      int64    `json:"min"`
	Max      int64    `json:"max"`
	Default  *int64   `json:"default,omitempty"`
	Unit     string   `json:"unit,omitempty"`
	Value    *int64   `json:"value,omitempty"`
	Text     string   `json:"text,omitempty"`    // a text value, or what the fixture calls the current number
	Choices  []string `json:"choices,omitempty"` // choice: Choices[i] is the value Min+i
	MaxLen   int      `json:"maxLen,omitempty"`
	Standard bool     `json:"standard"`
	Error    string   `json:"error,omitempty"`
}

// rdmSensor is one sensor reading.
type rdmSensor struct {
	Number    int    `json:"number"`
	Name      string `json:"name"`
	Unit      string `json:"unit"`
	Value     *int   `json:"value"`
	NormalMin int    `json:"normalMin"`
	NormalMax int    `json:"normalMax"`
	Error     string `json:"error,omitempty"`
}

// standardSetting is a standard parameter the page offers when a fixture supports it.
type standardSetting struct {
	pid     uint16
	name    string
	kind    string
	min     int64
	max     int64
	choices []string
}

var standardSettings = []standardSetting{
	{artnet.PIDCurve, "Dimmer curve", "choice", 1, 1, nil}, // choices read from CURVE_DESCRIPTION
	{artnet.PIDDisplayInvert, "Display invert", "choice", 0, 2, []string{"Off", "On", "Auto"}},
	{artnet.PIDDisplayLevel, "Display level", "number", 0, 255, nil},
	{artnet.PIDPanInvert, "Pan invert", "bool", 0, 1, nil},
	{artnet.PIDTiltInvert, "Tilt invert", "bool", 0, 1, nil},
	{artnet.PIDPanTiltSwap, "Pan/tilt swap", "bool", 0, 1, nil},
}

func findStandard(pid uint16) (standardSetting, bool) {
	for _, s := range standardSettings {
		if s.pid == pid {
			return s, true
		}
	}
	return standardSetting{}, false
}

// Parameter descriptions do not change for a given model and software version,
// so they are read once per run.
var paramDescCache sync.Map // "manufacturer/model/software/pid" → artnet.ParamDescription

type fixtureRef struct {
	ctl *artnet.Controller
	ip  string
	pa  artnet.PortAddress
	uid artnet.UID
}

func (r fixtureRef) get(pid uint16, data []byte) ([]byte, error) {
	return r.ctl.Get(r.ip, r.pa, r.uid, pid, data)
}

func openFixture(ip string, port int, uid string) (fixtureRef, error) {
	u, err := artnet.ParseUID(uid)
	if err != nil {
		return fixtureRef{}, err
	}
	pa, err := rdmTarget(ip, port)
	if err != nil {
		return fixtureRef{}, err
	}
	ctl, err := rdmController()
	if err != nil {
		return fixtureRef{}, err
	}
	return fixtureRef{ctl, ip, pa, u}, nil
}

func (r fixtureRef) describe(info artnet.DeviceInfo, pid uint16) (artnet.ParamDescription, error) {
	key := fmt.Sprintf("%04X/%04X/%d/%04X", r.uid.Manufacturer(), info.Model, info.SoftwareVersion, pid)
	if d, ok := paramDescCache.Load(key); ok {
		return d.(artnet.ParamDescription), nil
	}
	raw, err := r.get(artnet.PIDParameterDescription, artnet.EncodeNumber(int64(pid), 2))
	if err != nil {
		return artnet.ParamDescription{}, err
	}
	d, err := artnet.ParseParamDescription(raw)
	if err != nil {
		return d, err
	}
	paramDescCache.Store(key, d)
	return d, nil
}

func (r fixtureRef) info() (artnet.DeviceInfo, error) {
	d, err := r.get(artnet.PIDDeviceInfo, nil)
	if err != nil {
		return artnet.DeviceInfo{}, err
	}
	return artnet.ParseDeviceInfo(d)
}

func signedType(t byte) bool { return t == artnet.DataS8 || t == artnet.DataS16 || t == artnet.DataS32 }

// manufacturerSetting reads one manufacturer parameter's description and value.
func (r fixtureRef) manufacturerSetting(info artnet.DeviceInfo, pid uint16) rdmSetting {
	s := rdmSetting{PID: int(pid), Name: fmt.Sprintf("Parameter 0x%04X", pid), Kind: "number"}
	d, err := r.describe(info, pid)
	if err != nil {
		s.Error, s.ReadOnly = err.Error(), true
		return s
	}
	if d.Description != "" {
		s.Name = d.Description
	}
	s.Min, s.Max, s.ReadOnly = d.Min, d.Max, !d.CanSet()
	s.Unit = artnet.UnitName(d.Unit)
	if p := artnet.PrefixPower(d.Prefix); p != 0 {
		s.Unit = strings.TrimSpace(fmt.Sprintf("×10^%d %s", p, s.Unit))
	}
	switch {
	case d.DataType == artnet.DataASCII:
		s.Kind, s.MaxLen = "text", d.Size
	case !d.Numeric():
		s.Kind, s.ReadOnly = "raw", true
	case d.Min == 0 && d.Max == 1:
		s.Kind = "bool"
	}
	if s.Kind != "text" && s.Kind != "raw" {
		def := d.Default
		s.Default = &def
	}
	if !d.CanGet() {
		if d.CanSet() {
			s.Kind = "action"
		}
		return s
	}
	v, err := r.get(pid, nil)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	switch s.Kind {
	case "text":
		s.Text = artnet.Text(v)
	case "raw":
		s.Text = fmt.Sprintf("%x", v)
	default:
		if n, err := artnet.DecodeNumber(v, signedType(d.DataType)); err == nil {
			s.Value = &n
		} else {
			s.Error = err.Error()
		}
	}
	return s
}

// standardValue reads one standard setting.
func (r fixtureRef) standardValue(st standardSetting) rdmSetting {
	s := rdmSetting{PID: int(st.pid), Name: st.name, Kind: st.kind, Min: st.min, Max: st.max, Choices: st.choices, Standard: true}
	v, err := r.get(st.pid, nil)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	if len(v) < 1 {
		s.Error = "empty reply"
		return s
	}
	n := int64(v[0])
	s.Value = &n
	if st.pid == artnet.PIDCurve && len(v) >= 2 {
		s.Max = int64(v[1])
		s.Choices = nil
		for c := 1; c <= int(v[1]); c++ {
			name := fmt.Sprintf("Curve %d", c)
			if d, err := r.get(artnet.PIDCurveDescription, []byte{byte(c)}); err == nil && len(d) > 1 {
				name = artnet.Text(d[1:])
			}
			s.Choices = append(s.Choices, name)
		}
	}
	return s
}

// fixtureDetails reads everything the fixture page shows.
func fixtureDetails(ip string, port int, uid string) (obj, error) {
	r, err := openFixture(ip, port, uid)
	if err != nil {
		return nil, err
	}
	f := readFixture(r.ctl, ip, r.pa, r.uid)
	if f.Error != "" {
		return nil, errors.New(f.Error)
	}
	info, err := r.info()
	if err != nil {
		return nil, err
	}
	out := obj{"fixture": f}
	supported := map[uint16]bool{}
	var pids []uint16
	if d, err := r.get(artnet.PIDSupportedParameters, nil); err == nil {
		pids = artnet.PIDList(d)
		for _, p := range pids {
			supported[p] = true
		}
	} else {
		out["supportedError"] = err.Error()
	}
	u32 := func(pid uint16) any {
		if !supported[pid] {
			return nil
		}
		d, err := r.get(pid, nil)
		if err != nil || len(d) < 4 {
			return nil
		}
		return int64(d[0])<<24 | int64(d[1])<<16 | int64(d[2])<<8 | int64(d[3])
	}
	out["hours"], out["lampHours"] = u32(artnet.PIDDeviceHours), u32(artnet.PIDLampHours)
	out["canFactoryReset"], out["canRestart"] = supported[artnet.PIDFactoryDefaults], supported[artnet.PIDResetDevice]
	if supported[artnet.PIDFactoryDefaults] { // not shown: a Vortex 8S reported 0 even straight after a factory reset
		if d, err := r.get(artnet.PIDFactoryDefaults, nil); err == nil && len(d) == 1 {
			out["atDefaults"] = d[0] == 1
		}
	}

	sensors := []rdmSensor{}
	if supported[artnet.PIDSensorDefinition] || supported[artnet.PIDSensorValue] {
		for n := 0; n < int(info.Sensors); n++ {
			sn := rdmSensor{Number: n, Name: fmt.Sprintf("Sensor %d", n+1)}
			if d, err := r.get(artnet.PIDSensorDefinition, []byte{byte(n)}); err == nil {
				if def, err := artnet.ParseSensorDefinition(d); err == nil {
					sn.Name, sn.Unit, sn.NormalMin, sn.NormalMax = def.Description, artnet.UnitName(def.Unit), def.NormalMin, def.NormalMax
				}
			}
			if d, err := r.get(artnet.PIDSensorValue, []byte{byte(n)}); err == nil {
				if v, err := artnet.SensorValue(d); err == nil {
					sn.Value = &v
				}
			} else {
				sn.Error = err.Error()
			}
			sensors = append(sensors, sn)
		}
	}
	out["sensors"] = sensors

	settings := []rdmSetting{}
	for _, st := range standardSettings {
		if supported[st.pid] {
			settings = append(settings, r.standardValue(st))
		}
	}
	for _, p := range pids {
		if p >= 0x8000 {
			settings = append(settings, r.manufacturerSetting(info, p))
		}
	}
	out["settings"] = pairNames(settings)
	return out, nil
}

// pairNames folds a read-only "<X> Name" text into the number X it names (the
// Vortex has "DMX Smoothing" 1 and "DMX Smoothing Name" "Off").
func pairNames(in []rdmSetting) []rdmSetting {
	numbers := map[string]bool{}
	for _, s := range in {
		if s.Kind != "text" {
			numbers[s.Name] = true
		}
	}
	names := map[string]string{}
	for _, s := range in {
		if base, ok := strings.CutSuffix(s.Name, " Name"); ok && s.Kind == "text" && s.ReadOnly && numbers[base] {
			names[base] = s.Text
		}
	}
	out := make([]rdmSetting, 0, len(in))
	for _, s := range in {
		if base, ok := strings.CutSuffix(s.Name, " Name"); ok && s.Kind == "text" && s.ReadOnly && numbers[base] {
			continue
		}
		if t, ok := names[s.Name]; ok {
			s.Text = t
		}
		out = append(out, s)
	}
	return out
}

// setFixtureParam sends one setting and returns it as read back.
func setFixtureParam(ip string, port int, uid string, pid uint16, value any) (rdmSetting, error) {
	r, err := openFixture(ip, port, uid)
	if err != nil {
		return rdmSetting{}, err
	}
	if err := checkFixturePort(ip, port, r.pa, r.uid); err != nil {
		return rdmSetting{}, err
	}
	var data []byte
	var reread func() rdmSetting
	if st, ok := findStandard(pid); ok {
		n, err := wantNumber(value)
		if err != nil {
			return rdmSetting{}, err
		}
		if pid != artnet.PIDCurve && (n < st.min || n > st.max) {
			return rdmSetting{}, fmt.Errorf("%s takes %d-%d, not %d", st.name, st.min, st.max, n)
		}
		if pid == artnet.PIDCurve && (n < 1 || n > 255) {
			return rdmSetting{}, fmt.Errorf("curve %d is not 1-255", n)
		}
		data = []byte{byte(n)}
		reread = func() rdmSetting { return r.standardValue(st) }
	} else if pid >= 0x8000 {
		info, err := r.info()
		if err != nil {
			return rdmSetting{}, err
		}
		d, err := r.describe(info, pid)
		if err != nil {
			return rdmSetting{}, err
		}
		if !d.CanSet() {
			return rdmSetting{}, fmt.Errorf("%s is read-only", d.Description)
		}
		if data, err = encodeParam(d, value); err != nil {
			return rdmSetting{}, err
		}
		reread = func() rdmSetting { return r.manufacturerSetting(info, pid) }
	} else {
		return rdmSetting{}, fmt.Errorf("parameter 0x%04X is not one this page sets", pid)
	}
	if err := r.ctl.Set(ip, r.pa, r.uid, pid, data); err != nil && !errors.Is(err, luminex.ErrDryRun) {
		return rdmSetting{}, err
	}
	return reread(), nil
}

func wantNumber(v any) (int64, error) {
	switch n := v.(type) {
	case float64:
		if n != float64(int64(n)) {
			return 0, fmt.Errorf("%v is not a whole number", n)
		}
		return int64(n), nil
	case bool:
		if n {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("expected a number, got %v", v)
}

func encodeParam(d artnet.ParamDescription, v any) ([]byte, error) {
	if d.DataType == artnet.DataASCII {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s takes text", d.Description)
		}
		if len(s) > d.Size {
			return nil, fmt.Errorf("%s takes at most %d characters", d.Description, d.Size)
		}
		for _, c := range s {
			if c < 0x20 || c > 0x7E {
				return nil, fmt.Errorf("%s takes plain ASCII only, not %q", d.Description, c)
			}
		}
		return []byte(s), nil
	}
	if !d.Numeric() || (d.Size != 1 && d.Size != 2 && d.Size != 4) {
		return nil, fmt.Errorf("%s has a data type this page cannot set", d.Description)
	}
	n, err := wantNumber(v)
	if err != nil {
		return nil, err
	}
	if n < d.Min || n > d.Max {
		return nil, fmt.Errorf("%s takes %d-%d, not %d", d.Description, d.Min, d.Max, n)
	}
	return artnet.EncodeNumber(n, d.Size), nil
}

// rereadAfter reads a fixture back after it was reset or restarted, which can
// take it off the line for a while: it keeps asking for up to `wait`.
func rereadAfter(r fixtureRef, wait time.Duration) (rdmFixture, error) {
	end := time.Now().Add(wait)
	for {
		f := readFixture(r.ctl, r.ip, r.pa, r.uid)
		if f.Error == "" || time.Now().After(end) {
			if f.Error != "" {
				return f, fmt.Errorf("sent, but the fixture has not answered since: %s", f.Error)
			}
			return f, nil
		}
		time.Sleep(time.Second)
	}
}

// fixtureAction sends a factory reset or a restart and reads the fixture back.
// With keepAddress a factory reset first reads the fixture's DMX address and
// sets it back once the fixture answers again (Nick, 17 Nov 2026: change many
// settings and the mode, but not the patch); the personality stays as the reset
// left it. note says what happened to the address. A fixture read back is
// returned even with an error, when the reset went but the address did not.
func fixtureAction(ip string, port int, uid string, action string, keepAddress bool) (f rdmFixture, note string, err error) {
	r, err := openFixture(ip, port, uid)
	if err != nil {
		return rdmFixture{}, "", err
	}
	if err := checkFixturePort(ip, port, r.pa, r.uid); err != nil {
		return rdmFixture{}, "", err
	}
	keep := 0 // the address to set back; 0: none
	if action == "factory-reset" && keepAddress {
		d, gerr := r.ctl.Get(ip, r.pa, r.uid, artnet.PIDDMXStartAddress, nil)
		if gerr != nil || len(d) < 2 {
			if gerr == nil {
				gerr = fmt.Errorf("%d bytes", len(d))
			}
			return rdmFixture{}, "", fmt.Errorf("could not read the address to keep, so nothing was sent: %v", gerr)
		}
		if a := int(d[0])<<8 | int(d[1]); a >= 1 && a <= 512 {
			keep = a
		} else {
			note = "no DMX address to keep" // 0xFFFF: no footprint
		}
	}
	var pid uint16
	var data []byte
	switch action {
	case "factory-reset":
		pid = artnet.PIDFactoryDefaults
	case "restart":
		pid, data = artnet.PIDResetDevice, []byte{0x01} // warm reset
	default:
		return rdmFixture{}, "", fmt.Errorf("unknown fixture action %q", action)
	}
	err = r.ctl.Set(ip, r.pa, r.uid, pid, data)
	if errors.Is(err, luminex.ErrDryRun) {
		if keep > 0 {
			luminex.Logf("DRY RUN: would then set %s back to address %d", uid, keep)
			note = fmt.Sprintf("would keep address %d", keep)
		}
		return readFixture(r.ctl, ip, r.pa, r.uid), note, nil
	}
	if err != nil {
		return rdmFixture{}, "", err
	}
	f, err = rereadAfter(r, 30*time.Second)
	if err != nil || keep == 0 {
		if err != nil && keep > 0 {
			err = fmt.Errorf("%v, so it is not back at address %d yet", err, keep)
		}
		return f, note, err
	}
	if f.Start != nil && *f.Start == keep {
		return f, fmt.Sprintf("address %d kept", keep), nil
	}
	if err := r.ctl.Set(ip, r.pa, r.uid, artnet.PIDDMXStartAddress, []byte{byte(keep >> 8), byte(keep)}); err != nil {
		return f, "", fmt.Errorf("factory reset done, but the address did not go back to %d: %v", keep, err)
	}
	f = readFixture(r.ctl, ip, r.pa, r.uid)
	if f.Start == nil || *f.Start != keep {
		got := "none"
		if f.Start != nil {
			got = strconv.Itoa(*f.Start)
		}
		return f, "", fmt.Errorf("factory reset done and address %d sent, but the fixture reads back %s", keep, got)
	}
	return f, fmt.Sprintf("address %d kept", keep), nil
}

func registerRDMParamRoutes(handle func(string, http.HandlerFunc)) {
	params := func(w http.ResponseWriter, r *http.Request) (string, int, string, bool) {
		ip := ipParam(w, r)
		if ip == "" {
			return "", 0, "", false
		}
		port := portParam(w, r)
		if port == 0 {
			return "", 0, "", false
		}
		return ip, port, r.PathValue("uid"), true
	}
	handle("GET /api/node/{ip}/rdm/{port}/fixture/{uid}/details", func(w http.ResponseWriter, r *http.Request) {
		ip, port, uid, good := params(w, r)
		if !good {
			return
		}
		d, err := fixtureDetails(ip, port, uid)
		if err != nil {
			deviceFail(w, "RDM details "+ip, err)
			return
		}
		ok(w, d)
	})
	handle("GET /api/node/{ip}/rdm/{port}/fixture/{uid}/slots", func(w http.ResponseWriter, r *http.Request) {
		ip, port, uid, good := params(w, r)
		if !good {
			return
		}
		slots, err := fixtureSlots(ip, port, uid)
		if err != nil {
			deviceFail(w, "RDM slots "+ip, err)
			return
		}
		ok(w, obj{"slots": slots})
	})
	handle("POST /api/node/{ip}/rdm/{port}/fixture/{uid}/param", func(w http.ResponseWriter, r *http.Request) {
		ip, port, uid, good := params(w, r)
		if !good {
			return
		}
		var body struct {
			PID   *int `json:"pid"`
			Value any  `json:"value"`
		}
		if err := decodeBody(r, &body); err != nil || body.PID == nil || body.Value == nil || *body.PID < 0 || *body.PID > 0xFFFF {
			fail(w, 400, errors.New(`send {"pid": n, "value": ...}`))
			return
		}
		s, err := setFixtureParam(ip, port, uid, uint16(*body.PID), body.Value)
		if err != nil {
			deviceFail(w, "RDM setting "+ip, err)
			return
		}
		ok(w, obj{"setting": s, "dryRun": luminex.DryRun})
	})
	for _, action := range []string{"factory-reset", "restart"} {
		handle("POST /api/node/{ip}/rdm/{port}/fixture/{uid}/"+action, func(w http.ResponseWriter, r *http.Request) {
			ip, port, uid, good := params(w, r)
			if !good {
				return
			}
			var body struct {
				KeepAddress bool `json:"keepAddress"` // factory reset only: set the DMX address back afterwards
			}
			if err := decodeBody(r, &body); err != nil {
				fail(w, 400, fmt.Errorf("bad body: %v", err))
				return
			}
			f, note, err := fixtureAction(ip, port, uid, action, body.KeepAddress)
			if f.UID != "" && f.Error == "" {
				rdmStoreFixture(ip, port, f) // also when the reset went but the address did not: every page shows the fixture as it is
			}
			if err != nil {
				deviceFail(w, "RDM "+action+" "+ip, err)
				return
			}
			out := obj{"fixture": f, "dryRun": luminex.DryRun}
			if note != "" {
				out["note"] = note
			}
			ok(w, out)
		})
	}
}
