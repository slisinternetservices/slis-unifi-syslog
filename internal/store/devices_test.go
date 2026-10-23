package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDeviceInventory(t *testing.T) {
	dir := t.TempDir()
	m, err := OpenMeta(filepath.Join(dir, "meta.db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_800_000_000, 0)
	m.TouchDevice("U6-Pro", "U6-Pro", "F4:E2:C6:00:00:01", "6.6.55", "Office", "84.1.2.3", t0)
	// Same NATed source IP, different device.
	m.TouchDevice("USW-24", "USW-24", "f4:e2:c6:00:00:02", "7.1.26", "Office", "84.1.2.3", t0)
	m.TouchDevice("UDM-Pro", "", "", "", "Office", "84.1.2.3", t0)
	m.TouchDevice("", "", "", "", "Office", "84.1.2.3", t0) // nothing to identify
	if err := m.FlushDevices(); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateDevice("f4:e2:c6:00:00:01", "AP Reception", "ceiling", true); err != nil {
		t.Fatal(err)
	}

	// Firmware upgrade a minute later must not lose the custom name.
	m.TouchDevice("U6-Pro", "U6-Pro", "f4:e2:c6:00:00:01", "6.7.10", "Office", "84.1.2.3", t0.Add(time.Minute))
	if err := m.FlushDevices(); err != nil {
		t.Fatal(err)
	}
	if err := m.reloadDevices(); err != nil {
		t.Fatal(err)
	}
	devs := map[string]InvDevice{}
	for _, d := range m.Devices() {
		devs[d.Key] = d
	}
	if len(devs) != 3 {
		t.Fatalf("want 3 devices, got %d: %+v", len(devs), devs)
	}
	ap := devs["f4:e2:c6:00:00:01"]
	if ap.Name != "AP Reception" || ap.Notes != "ceiling" {
		t.Errorf("custom fields lost: %+v", ap)
	}
	if ap.Version != "6.7.10" || ap.PrevVersion != "6.6.55" || ap.VersionChanged != t0.Add(time.Minute).Unix() {
		t.Errorf("firmware change not tracked: %+v", ap)
	}
	if ap.LastSeen != t0.Add(time.Minute).Unix() || ap.FirstSeen != t0.Unix() {
		t.Errorf("timestamps: %+v", ap)
	}
	if _, ok := devs[DeviceKey("", "UDM-Pro", "84.1.2.3")]; !ok {
		t.Errorf("device without MAC not keyed by hostname")
	}

	if err := m.SetDeviceSilent(ap.Key, true); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteDevice("f4:e2:c6:00:00:02"); err != nil {
		t.Fatal(err)
	}
	m.reloadDevices()
	if len(m.Devices()) != 2 {
		t.Errorf("delete failed")
	}
	for _, d := range m.Devices() {
		if d.Key == ap.Key && !d.Silent {
			t.Errorf("silent flag not persisted")
		}
	}
}
