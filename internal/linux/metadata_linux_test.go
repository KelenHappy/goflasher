//go:build linux

package linux

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadUSBAncestorSerial(t *testing.T) {
	root := t.TempDir()
	usb := filepath.Join(root, "usb1", "1-13")
	block := filepath.Join(usb, "1-13:1.0", "host8", "target8:0:0", "8:0:0:0", "block", "sdc")
	requireNoError(t, os.MkdirAll(block, 0755))
	write(t, filepath.Join(usb, "idVendor"), "090c")
	write(t, filepath.Join(usb, "idProduct"), "1000")
	write(t, filepath.Join(usb, "serial"), "AA0OO7RP1MRHMVZW")
	if got := readUSBAncestorAttribute(block, "serial"); got != "AA0OO7RP1MRHMVZW" {
		t.Fatalf("USB ancestor serial = %q", got)
	}
}

func TestMetadataAcceptsMatchingUSBSerialWhenSCSISerialDiffers(t *testing.T) {
	root := t.TempDir()
	usb := filepath.Join(root, "usb1", "1-13")
	real := filepath.Join(usb, "1-13:1.0", "host8", "target8:0:0", "8:0:0:0", "block", "sdc")
	class := filepath.Join(root, "class", "sdc")
	requireNoError(t, os.MkdirAll(filepath.Join(real, "device"), 0755))
	requireNoError(t, os.MkdirAll(class, 0755))
	write(t, filepath.Join(usb, "idVendor"), "090c")
	write(t, filepath.Join(usb, "idProduct"), "1000")
	write(t, filepath.Join(usb, "serial"), "USB-SERIAL")
	write(t, filepath.Join(class, "device", "serial"), "SCSI-SERIAL")
	write(t, filepath.Join(class, "dev"), "8:32")
	write(t, filepath.Join(class, "size"), "65536")
	req := privilegedRequest{Identity: "USB-SERIAL", Serial: "USB-SERIAL", Major: 8, Minor: 32, Capacity: 65536 * 512, Mode: modeWrite}
	if _, _, err := validateDeviceMetadata(req, class, real); err != nil {
		t.Fatalf("matching physical USB serial rejected: %v", err)
	}
	req.Identity, req.Serial = "REPLACED", "REPLACED"
	if _, _, err := validateDeviceMetadata(req, class, real); !errors.Is(err, ErrDeviceChanged) {
		t.Fatalf("replacement error = %v", err)
	}
}

func TestParseMountInfo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mountinfo")
	os.WriteFile(p, []byte("42 1 8:17 / /media/My\\040USB rw - vfat /dev/sdb1 rw\n"), 0600)
	m, err := parseMountInfo(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := m[devNumber{8, 17}][0]; got != "/media/My USB" {
		t.Fatalf("mount = %q", got)
	}
}

func TestUdevPropertiesReadRuntimeDatabaseWithoutCLI(t *testing.T) {
	root := t.TempDir()
	b := &Backend{UdevDataRoot: root}
	write(t, filepath.Join(root, "b8:16"), "I:ignored\nE:ID_BUS=usb\nE:ID_SERIAL_SHORT=SERIAL=WITH=EQUALS\nE:MALFORMED\n")

	properties := b.udev(8, 16)
	if properties["ID_BUS"] != "usb" || properties["ID_SERIAL_SHORT"] != "SERIAL=WITH=EQUALS" {
		t.Fatalf("udev properties = %#v", properties)
	}
	if _, ok := properties["I:ignored"]; ok {
		t.Fatal("non-environment udev record was parsed as a property")
	}
	if got := b.udev(8, 99); len(got) != 0 {
		t.Fatalf("missing udev record = %#v, want empty supplementary metadata", got)
	}
}
