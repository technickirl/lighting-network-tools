package luminex

import "testing"

// TXT records as a real GigaCore 16tf and LumiNode 12 would send them without
// their serial and mac fields (real ones include both; the instance name is
// the serial either way).
var realGen2 = map[string]string{"txtvers": "1", "product_type": "GigaCore16tf", "device_name": "GigaCore 16tf",
	"ip_address": "192.168.5.50/16", "ip_address_info": "config_allowed"}
var realLumiNode = map[string]string{"product_type": "LumiNode12", "device_name": "LumiNode73",
	"ip_address": "192.168.1.73/16", "ip_address_info": "input+output+config_allowed"}

func TestParseTxtRealDevices(t *testing.T) {
	g := ParseTxt(realGen2)
	if g.Family != "gigacore" || g.Generation != "gen2" || g.APIKind != "gen2" || g.IP != "192.168.5.50" || g.Prefix != 16 {
		t.Errorf("gen2: %+v", g)
	}
	l := ParseTxt(realLumiNode)
	if l.Family != "luminode" || l.APIKind != "luminode" || l.Name != "LumiNode73" || l.IP != "192.168.1.73" {
		t.Errorf("luminode: %+v", l)
	}
	if g.Serial != "" || g.MAC != "" {
		t.Errorf("real TXT has no serial/mac, got %q %q", g.Serial, g.MAC)
	}
}

func TestDeviceIDAndSameDevice(t *testing.T) {
	before := ParseTxt(realGen2)
	before.Instance = "LUM546436890"
	before.ID = deviceID(before)
	if before.ID != "LUM546436890" {
		t.Fatalf("ID should fall back to the instance name, got %q", before.ID)
	}
	// Same switch after a factory reset: new address, new name, same instance.
	after := ParseTxt(map[string]string{"product_type": "GigaCore16tf", "device_name": "GigaCore16tf", "ip_address": "2.0.0.1/8"})
	after.Instance = "LUM546436890"
	after.ID = deviceID(after)
	if !SameDevice(before, after) {
		t.Errorf("same instance name should be the same device")
	}
	other := after
	other.Instance, other.ID = "LUM344175502", "LUM344175502"
	if SameDevice(before, other) {
		t.Errorf("different instance name should not match")
	}
	// Fakes send serial and mac: serial wins.
	fake := ParseTxt(map[string]string{"serial": "LUM838009434", "mac": "aa:bb:cc:00:00:01"})
	fake.Instance = "LumiNode4 Fake A"
	if deviceID(fake) != "LUM838009434" {
		t.Errorf("serial should win: %q", deviceID(fake))
	}
	if SameDevice(Device{}, Device{}) {
		t.Errorf("empty IDs must never match")
	}
}
