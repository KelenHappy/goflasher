//go:build linux

package udisks

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestBlockForDeviceUsesDeviceProperty(t *testing.T) {
	block := dbus.ObjectPath("/org/freedesktop/UDisks2/block_devices/mmcblk0p1")
	drive := dbus.ObjectPath("/org/freedesktop/UDisks2/drives/Card_Reader")
	objects := managedObjects{
		block: {blockInterface: {
			"Device": dbus.MakeVariant([]byte("/dev/mmcblk0p1\x00")),
			"Drive":  dbus.MakeVariant(drive),
		}},
	}
	gotBlock, gotDrive, err := blockForDevice(objects, "/dev/mmcblk0p1")
	if err != nil {
		t.Fatal(err)
	}
	if gotBlock != block || gotDrive != drive {
		t.Fatalf("blockForDevice() = %q, %q; want %q, %q", gotBlock, gotDrive, block, drive)
	}
}

func TestBlockForDeviceRejectsUnknownOrMalformedProperties(t *testing.T) {
	objects := managedObjects{
		"/wrong-type": {blockInterface: {"Device": dbus.MakeVariant("/dev/sdb")}},
		"/other":      {blockInterface: {"Device": dbus.MakeVariant([]byte("/dev/sdc\x00"))}},
	}
	if _, _, err := blockForDevice(objects, "/dev/sdb"); err == nil {
		t.Fatal("malformed Device property was accepted")
	}
}
