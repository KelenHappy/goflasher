//go:build windows

package windows

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"

	"github.com/goflasher/goflasher/internal/device"
	"golang.org/x/sys/windows"
)

func TestQueryStorageDescriptorTwoPhase(t *testing.T) {
	var sizes []int
	b, err := queryStorageDescriptor(func(out []byte) (uint32, error) {
		sizes = append(sizes, len(out))
		binary.LittleEndian.PutUint32(out[4:8], 40)
		if len(out) < 40 {
			return storageDescriptorHeaderSize, nil
		}
		return 40, nil
	})
	requireNoError(t, err)
	requireEqual(t, "len", len(b), 40)
	requireEqual(t, "buffer sizes", fmt.Sprint(sizes), fmt.Sprint([]int{storageDescriptorHeaderSize, 40}))
}

func TestQueryStorageDescriptorRejectsOversizedByteCount(t *testing.T) {
	calls := 0
	_, err := queryStorageDescriptor(func(out []byte) (uint32, error) {
		calls++
		if calls == 1 {
			binary.LittleEndian.PutUint32(out[4:8], 40)
			return storageDescriptorHeaderSize, nil
		}
		return uint32(len(out) + 1), nil
	})
	if err == nil {
		t.Fatal("oversized storage descriptor byte count accepted")
	}
	requireErrorContains(t, err, "invalid storage descriptor byte count")
}

func TestQueryStorageDescriptorGrowsOnBufferError(t *testing.T) {
	calls := 0
	b, err := queryStorageDescriptor(func(out []byte) (uint32, error) {
		calls++
		if len(out) < 512 {
			return 0, windows.ERROR_INSUFFICIENT_BUFFER
		}
		return 100, nil
	})
	requireNoError(t, err)
	requireEqual(t, "len", len(b), 100)
	if calls < 3 {
		t.Fatalf("calls=%d, want growth through at least 3 buffers", calls)
	}
}

func TestQueryStorageDescriptorRejectsBadHeader(t *testing.T) {
	for _, size := range []uint32{0, 7, maxStorageDescriptorSize + 1} {
		_, err := queryStorageDescriptor(func(out []byte) (uint32, error) {
			binary.LittleEndian.PutUint32(out[4:8], size)
			return storageDescriptorHeaderSize, nil
		})
		if err == nil {
			t.Fatalf("descriptor size %d accepted", size)
		}
	}
}

func TestStoragePropertyQueryLayout(t *testing.T) {
	q := storageDeviceIDProperty.request()
	requireEqual(t, "len", len(q), storagePropertyQuerySize)
	requireEqual(t, "PropertyId", binary.LittleEndian.Uint32(q[0:4]), uint32(storageDeviceIDProperty))
	requireEqual(t, "QueryType", binary.LittleEndian.Uint32(q[4:8]), uint32(propertyStandardQuery))
}

func TestParseHotplugInfo(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want hotplugFlags
	}{
		{"device hotplug", []byte{8, 0, 0, 0, 1, 0, 1, 0}, hotplugFlags{deviceHotplug: true}},
		{"media hotplug", []byte{8, 0, 0, 0, 0, 1, 0, 0}, hotplugFlags{mediaHotplug: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hot, err := parseHotplugInfo(tt.data)
			requireNoError(t, err)
			requireEqual(t, "flags", hot, tt.want)
		})
	}
	if _, err := parseHotplugInfo(make([]byte, 7)); err == nil {
		t.Fatal("short STORAGE_HOTPLUG_INFO accepted")
	}
}

func TestParseDiskGeometryExSize(t *testing.T) {
	b := make([]byte, 32)
	binary.LittleEndian.PutUint64(b[24:], 16<<30)
	size, err := parseDiskGeometryExSize(b)
	requireNoError(t, err)
	requireEqual(t, "size", size, uint64(16<<30))
	if _, err := parseDiskGeometryExSize(b[:31]); err == nil {
		t.Fatal("short DISK_GEOMETRY_EX accepted")
	}
}

func TestParseDiskGeometryBytesPerSector(t *testing.T) {
	b := make([]byte, 32)
	binary.LittleEndian.PutUint32(b[20:], 4096)
	size, err := parseDiskGeometryBytesPerSector(b)
	requireNoError(t, err)
	requireEqual(t, "bytes per sector", size, uint32(4096))
	if _, err := parseDiskGeometryBytesPerSector(b[:23]); err == nil {
		t.Fatal("short DISK_GEOMETRY accepted")
	}
}

func TestDiskRecordFromEvidence(t *testing.T) {
	const vendor, model, serial = "Vendor", "Model X", " SN 42 "
	const rawWWN, wantWWN = "5000c50012345678", "5000C50012345678"
	q := make([]byte, storageDeviceDescriptorSize)
	off := func(s string) uint32 {
		o := uint32(len(q))
		q = append(q, []byte(s)...)
		q = append(q, 0)
		return o
	}
	vendorOff, modelOff, serialOff := off(vendor), off(model), off(serial)
	binary.LittleEndian.PutUint32(q[12:16], vendorOff)
	binary.LittleEndian.PutUint32(q[16:20], modelOff)
	binary.LittleEndian.PutUint32(q[24:28], serialOff)
	binary.LittleEndian.PutUint32(q[28:32], uint32(busTypeUSB))
	ev := diskEvidence{
		number:     3,
		length:     1 << 30,
		descriptor: q,
		wwn:        rawWWN,
		hotplug:    hotplugFlags{deviceHotplug: true},
	}
	r, err := diskRecordFromEvidence(ev)
	if err != nil {
		t.Fatal(err)
	}
	const wantSerial = "SN 42"
	want := diskRecord{
		Device: device.Device{
			ID:        "windows:serial=" + wantSerial + ";wwn=" + wantWWN,
			Path:      `\\.\PhysicalDrive3`,
			Vendor:    vendor,
			Model:     model,
			Serial:    wantSerial,
			WWN:       wantWWN,
			Transport: "usb",
			Major:     3,
			Size:      1 << 30,
		},
		identity:      windowsIdentityEvidence{Serial: wantSerial, WWN: wantWWN},
		deviceHotplug: true,
		usbAncestor:   true,
		deviceNumber:  3,
	}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("record=%+v\nwant  =%+v", r, want)
	}
	if _, err := diskRecordFromEvidence(diskEvidence{number: 3, length: 1, descriptor: q[:35]}); err == nil {
		t.Fatal("short STORAGE_DEVICE_DESCRIPTOR accepted")
	}
}
