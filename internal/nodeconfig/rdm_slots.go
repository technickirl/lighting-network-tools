package nodeconfig

// What each DMX channel of a fixture does, read over RDM (SLOT_INFO and
// SLOT_DESCRIPTION, both GETs), so the sACN controller can patch the fixture with
// a profile that knows its dimmer from its red. Many fixtures answer neither; then
// there is nothing to go on and the caller falls back to plain numbered channels.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/technickirl/lighting-network-tools/internal/artnet"
)

// rdmSlot is one channel in the sACN controller's terms: Type is one of its
// channel types (internal/sacn ChannelTypes).
type rdmSlot struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// slotLabels: E1.20 table C-2, slot label IDs of primary slots, as a name and
// the controller's channel type.
var slotLabels = map[uint16][2]string{
	0x0001: {"Dimmer", "dimmer"}, 0x0002: {"Master", "dimmer"},
	0x0101: {"Pan", "pan"}, 0x0102: {"Tilt", "tilt"},
	0x0201: {"Color wheel", "other"}, 0x0202: {"Cyan", "cyan"}, 0x0203: {"Yellow", "yellow"}, 0x0204: {"Magenta", "magenta"},
	0x0205: {"Red", "red"}, 0x0206: {"Green", "green"}, 0x0207: {"Blue", "blue"}, 0x0208: {"Color correction", "cct"},
	0x0209: {"Color scroll", "other"}, 0x0210: {"Color semaphore", "other"}, 0x0211: {"Amber", "amber"}, 0x0212: {"White", "white"},
	0x0213: {"Warm white", "warm"}, 0x0214: {"Cool white", "cool"}, 0x0215: {"UV", "uv"}, 0x0216: {"Hue", "other"}, 0x0217: {"Saturation", "other"},
	0x0301: {"Static gobo", "other"}, 0x0302: {"Rotating gobo", "other"}, 0x0303: {"Prism", "other"}, 0x0304: {"Effects wheel", "other"},
	0x0401: {"Iris", "other"}, 0x0402: {"Edge", "other"}, 0x0403: {"Frost", "other"}, 0x0404: {"Strobe", "strobe"}, 0x0405: {"Zoom", "zoom"},
	0x0406: {"Framing shutter", "other"}, 0x0407: {"Shutter rotate", "other"}, 0x0408: {"Douser", "other"}, 0x0409: {"Barn door", "other"},
	0x0501: {"Lamp control", "other"}, 0x0502: {"Fixture control", "other"}, 0x0503: {"Fixture speed", "other"}, 0x0504: {"Macro", "other"},
}

// slotWords guesses a type from a channel's name, for a fixture that names its
// slots but does not label them. Order matters: "warm white" before "white".
var slotWords = []struct {
	re *regexp.Regexp
	t  string
}{
	{regexp.MustCompile(`(?i)fine|16.?bit lsb|\blsb\b`), "other"},
	{regexp.MustCompile(`(?i)dim|intens|master`), "dimmer"},
	{regexp.MustCompile(`(?i)warm`), "warm"}, {regexp.MustCompile(`(?i)cool|cold`), "cool"},
	{regexp.MustCompile(`(?i)\bred\b`), "red"}, {regexp.MustCompile(`(?i)green`), "green"}, {regexp.MustCompile(`(?i)blue`), "blue"},
	{regexp.MustCompile(`(?i)cyan`), "cyan"}, {regexp.MustCompile(`(?i)magenta`), "magenta"}, {regexp.MustCompile(`(?i)yellow`), "yellow"},
	{regexp.MustCompile(`(?i)amber`), "amber"}, {regexp.MustCompile(`(?i)lime`), "lime"}, {regexp.MustCompile(`(?i)\buv\b|ultra`), "uv"},
	{regexp.MustCompile(`(?i)white`), "white"}, {regexp.MustCompile(`(?i)cct|colou?r temp|kelvin`), "cct"},
	{regexp.MustCompile(`(?i)\bpan\b`), "pan"}, {regexp.MustCompile(`(?i)tilt`), "tilt"}, {regexp.MustCompile(`(?i)zoom`), "zoom"},
	{regexp.MustCompile(`(?i)strobe|shutter`), "strobe"},
}

// slotChannels turns what a fixture said into one channel per slot of its footprint.
// names is SLOT_DESCRIPTION by offset, where the fixture gave one.
func slotChannels(footprint int, slots []artnet.Slot, names map[int]string) []rdmSlot {
	out := make([]rdmSlot, footprint)
	for i := range out {
		out[i] = rdmSlot{Name: fmt.Sprintf("Ch %d", i+1), Type: "other"}
	}
	labelled := map[int]bool{}
	for _, s := range slots {
		i := int(s.Offset)
		if i >= footprint || s.Type != 0 {
			continue
		}
		if l, ok := slotLabels[s.Label]; ok {
			out[i], labelled[i] = rdmSlot{Name: l[0], Type: l[1]}, true
		}
	}
	for _, s := range slots { // secondary slots, once their primaries have names
		i, of := int(s.Offset), int(s.Label)
		if i < footprint && s.Type != 0 && of < footprint && of != i {
			what := "2nd"
			if s.Type == 0x01 {
				what = "fine"
			}
			out[i], labelled[i] = rdmSlot{Name: out[of].Name + " " + what, Type: "other"}, true
		}
	}
	for i, n := range names {
		n = strings.TrimSpace(n)
		if i >= footprint || n == "" {
			continue
		}
		if len(n) > 40 {
			n = n[:40]
		}
		out[i].Name = n
		if !labelled[i] {
			for _, w := range slotWords {
				if w.re.MatchString(n) {
					out[i].Type = w.t
					break
				}
			}
		}
	}
	return out
}

// fixtureSlots asks a fixture what its channels are. An error means it told us nothing.
func fixtureSlots(ip string, port int, uid string) ([]rdmSlot, error) {
	r, err := openFixture(ip, port, uid)
	if err != nil {
		return nil, err
	}
	info, err := r.info()
	if err != nil {
		return nil, err
	}
	footprint := int(info.Footprint)
	if footprint < 1 || footprint > 512 {
		return nil, fmt.Errorf("the fixture has no DMX channels")
	}
	var slots []artnet.Slot
	d, infoErr := r.get(artnet.PIDSlotInfo, nil)
	if infoErr == nil {
		slots = artnet.ParseSlotInfo(d)
	}
	// Names take a request a channel: only for a fixture small enough, and not at all once one fails.
	names := map[int]string{}
	if footprint <= 64 {
		for i := 0; i < footprint; i++ {
			d, err := r.get(artnet.PIDSlotDescription, artnet.EncodeNumber(int64(i), 2))
			if err != nil {
				if i == 0 {
					break
				}
				continue
			}
			if len(d) > 2 {
				names[i] = artnet.Text(d[2:])
			}
		}
	}
	if len(slots) == 0 && len(names) == 0 {
		return nil, fmt.Errorf("the fixture does not say what its channels are (no SLOT_INFO or SLOT_DESCRIPTION): %v", infoErr)
	}
	return slotChannels(footprint, slots, names), nil
}
