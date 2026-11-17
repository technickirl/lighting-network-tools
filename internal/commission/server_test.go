package commission

import (
	"testing"

	"github.com/technickirl/lighting-network-tools/internal/luminex"
)

func TestValidMask(t *testing.T) {
	good := []string{"255.0.0.0", "255.255.0.0", "255.255.255.0", "255.255.255.252", "128.0.0.0"}
	bad := []string{"", "255.255.255.1", "255.0.255.0", "0.0.0.0", "255.255.255.255", "ffff::", "255.255.0", "16"}
	for _, m := range good {
		if !validMask(m) {
			t.Errorf("%q should be a valid mask", m)
		}
	}
	for _, m := range bad {
		if validMask(m) {
			t.Errorf("%q should be rejected", m)
		}
	}
	if maskToCIDR("255.255.0.0") != 16 {
		t.Errorf("maskToCIDR(255.255.0.0) = %d", maskToCIDR("255.255.0.0"))
	}
}

func TestDedupe(t *testing.T) {
	got := dedupe([]string{"a", "b", "a", "c", "b"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("dedupe: %v", got)
	}
}

func TestMergeSeenReplacesRealDevice(t *testing.T) {
	state.mu.Lock()
	state.devices = []luminex.Device{realDevice("2.0.0.1")}
	state.mu.Unlock()
	mergeSeen([]luminex.Device{realDevice("192.168.4.3")})
	mergeSeen([]luminex.Device{realDevice("192.168.4.3")})
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.devices) != 1 || state.devices[0].IP != "192.168.4.3" {
		t.Errorf("expected one row at the new address, got %+v", state.devices)
	}
}
