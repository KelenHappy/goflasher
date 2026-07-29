package device

import "testing"

func TestSameIdentity(t *testing.T) {
	base := Device{ID: "usb-serial", Path: "/dev/sdb", Serial: "ABC", Major: 8, Minor: 16, SysfsPath: "/sys/devices/usb/sdb"}
	if !SameIdentity(base, base) {
		t.Fatal("identical devices differ")
	}
	changed := base
	changed.Serial = "XYZ"
	if SameIdentity(base, changed) {
		t.Fatal("serial change accepted")
	}
	changed = base
	changed.Minor = 32
	if SameIdentity(base, changed) {
		t.Fatal("device number change accepted")
	}
	withoutSerial := base
	withoutSerial.Serial = ""
	if !SameIdentity(withoutSerial, withoutSerial) {
		t.Fatal("sysfs fallback rejected")
	}
	withWWN := base
	withWWN.WWN = "wwn-1"
	changed = withWWN
	changed.WWN = "wwn-2"
	if SameIdentity(withWWN, changed) {
		t.Fatal("WWN change accepted")
	}
}
