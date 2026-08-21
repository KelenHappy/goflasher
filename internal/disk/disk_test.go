package disk

import "testing"

func TestSameIdentity(t *testing.T) {
	base := Disk{ID: "stable", Device: "/dev/example", Serial: "serial", Size: 1024}
	if !SameIdentity(base, base) {
		t.Fatal("identical disk snapshots did not match")
	}
	for name, changed := range map[string]Disk{
		"id":               {ID: "other", Device: base.Device, Serial: base.Serial, Size: base.Size},
		"device":           {ID: base.ID, Device: "/dev/other", Serial: base.Serial, Size: base.Size},
		"serial":           {ID: base.ID, Device: base.Device, Serial: "other", Size: base.Size},
		"size":             {ID: base.ID, Device: base.Device, Serial: base.Serial, Size: 2048},
		"registry ID":      withIdentityField(base, func(d *Disk) { d.RegistryID = "changed" }),
		"registry path":    withIdentityField(base, func(d *Disk) { d.RegistryPath = "changed" }),
		"media ID":         withIdentityField(base, func(d *Disk) { d.MediaID = "changed" }),
		"transport serial": withIdentityField(base, func(d *Disk) { d.TransportSerial = "changed" }),
		"WWN":              withIdentityField(base, func(d *Disk) { d.WWN = "changed" }),
	} {
		t.Run(name, func(t *testing.T) {
			if SameIdentity(base, changed) {
				t.Fatal("changed disk snapshots matched")
			}
		})
	}
}

func withIdentityField(d Disk, change func(*Disk)) Disk {
	change(&d)
	return d
}
