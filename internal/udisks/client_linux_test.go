//go:build linux

package udisks

import (
	"os"
	"path/filepath"
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

func TestResolveDevicePathResolvesPersistentAlias(t *testing.T) {
	root := t.TempDir()
	device := filepath.Join(root, "sdb1")
	if err := os.WriteFile(device, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "by-uuid", "test-uuid")
	if err := os.MkdirAll(filepath.Dir(alias), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(device, alias); err != nil {
		t.Fatal(err)
	}

	if got := resolveDevicePath(alias); got != device {
		t.Fatalf("resolveDevicePath(%q) = %q; want %q", alias, got, device)
	}

	block := dbus.ObjectPath("/org/freedesktop/UDisks2/block_devices/sdb1")
	objects := managedObjects{
		block: {blockInterface: {
			"Device": dbus.MakeVariant([]byte(device + "\x00")),
		}},
	}
	gotBlock, _, err := blockForDevice(objects, resolveDevicePath(alias))
	if err != nil {
		t.Fatal(err)
	}
	if gotBlock != block {
		t.Fatalf("blockForDevice() = %q; want %q", gotBlock, block)
	}
}

func TestResolveDevicePathRetainsUnresolvedPath(t *testing.T) {
	device := filepath.Join(t.TempDir(), "missing")
	if got := resolveDevicePath(device); got != device {
		t.Fatalf("resolveDevicePath(%q) = %q; want original path", device, got)
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
