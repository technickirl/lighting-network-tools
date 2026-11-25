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
		{"Dimmer", "dimmer"}, {"Dimmer fine", "other"}, {"LED Red", "red"}, {"Cyan", "cyan"},
		{"Warm White", "warm"}, {"Red fine", "other"}, {"Ch 7", "other"}, {"Ch 8", "other"},
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
}
