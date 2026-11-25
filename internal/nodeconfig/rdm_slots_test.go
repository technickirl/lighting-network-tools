package nodeconfig

import (
	"testing"

	"github.com/technickirl/lighting-network-tools/internal/artnet"
)

func TestSlotChannels(t *testing.T) {
	// Labelled slots, a fine byte, an undefined one named by its description, and one the fixture said nothing about.
	got := slotChannels(8, []artnet.Slot{
		{Offset: 0, Label: 0x0001}, {Offset: 1, Type: 1, Label: 0}, {Offset: 2, Label: 0x0205}, {Offset: 3, Label: 0x0202},
		{Offset: 4, Label: 0xFFFF}, {Offset: 5, Label: 0xFFFF}, {Offset: 9, Label: 0x0206},
	}, map[int]string{2: "LED Red", 4: "Warm White", 5: "Red fine", 6: "  "})
	want := []rdmSlot{
		{Name: "Dimmer", Type: "dimmer"}, {Name: "Dimmer fine", Type: "dimmer", Fine: true}, {Name: "LED Red", Type: "red"}, {Name: "Cyan", Type: "cyan"},
		// "Red fine" does not follow a red channel, so it is no one's low half.
		{Name: "Warm White", Type: "warm"}, {Name: "Red fine", Type: "other"}, {Name: "Ch 7", Type: "other"}, {Name: "Ch 8", Type: "other"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("channel %d: %+v, want %+v", i+1, got[i], want[i])
		}
	}
	// Names alone are enough to type the channels.
	got = slotChannels(4, nil, map[int]string{0: "Intensity", 1: "Colour Temp", 2: "Green/Magenta tint", 3: "Shutter"})
	for i, w := range []string{"dimmer", "cct", "green", "strobe"} {
		if got[i].Type != w {
			t.Errorf("%q: %s, want %s", got[i].Name, got[i].Type, w)
		}
	}
	// Pan and tilt sit at half, and a fine channel straight after its coarse one pairs with it (16 bit).
	got = slotChannels(6, []artnet.Slot{{Offset: 0, Label: 0x0101}, {Offset: 1, Type: 1, Label: 0}, {Offset: 2, Label: 0x0102}, {Offset: 3, Type: 1, Label: 2}}, map[int]string{4: "Zoom", 5: "Zoom Fine"})
	want = []rdmSlot{
		{Name: "Pan", Type: "pan", Default: 128}, {Name: "Pan fine", Type: "pan", Fine: true},
		{Name: "Tilt", Type: "tilt", Default: 128}, {Name: "Tilt fine", Type: "tilt", Fine: true},
		{Name: "Zoom", Type: "zoom"}, {Name: "Zoom Fine", Type: "zoom", Fine: true},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("16 bit, channel %d: %+v, want %+v", i+1, got[i], want[i])
		}
	}
	// Fine channels apart from their coarse ones (pan, tilt, pan fine, tilt fine) are not paired.
	got = slotChannels(4, []artnet.Slot{{Offset: 0, Label: 0x0101}, {Offset: 1, Label: 0x0102}, {Offset: 2, Type: 1, Label: 0}, {Offset: 3, Type: 1, Label: 1}}, nil)
	for i, w := range []rdmSlot{{Name: "Pan", Type: "pan", Default: 128}, {Name: "Tilt", Type: "tilt", Default: 128}, {Name: "Pan fine", Type: "other"}, {Name: "Tilt fine", Type: "other"}} {
		if got[i] != w {
			t.Errorf("split fine, channel %d: %+v, want %+v", i+1, got[i], w)
		}
	}
}
