//go:build linux

package linux

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/goflasher/goflasher/internal/device"
)

func TestEnumerationFiltersAndMounts(t *testing.T) {
	t.Run("returns only supported removable devices", func(t *testing.T) {
		b := newBackendFixture(t)
		devices, err := b.ListAllowedDevices(context.Background())
		requireNoError(t, err)
		requireDevicePaths(t, devices, "sdb", "sdc")
	})
	t.Run("reports all mounted flash partitions", func(t *testing.T) {
		b := newBackendFixture(t)
		flash, err := b.RefreshDevice(context.Background(), "FLASH123")
		requireNoError(t, err)
		assertMountedFlashMetadata(t, flash)
	})
	t.Run("identifies a card reader", func(t *testing.T) {
		b := newBackendFixture(t)
		card, err := b.RefreshDevice(context.Background(), "CARD123")
		requireNoError(t, err)
		if !card.IsCardReader {
			t.Fatalf("card not identified: %+v", card)
		}
	})
	for _, tc := range []struct {
		name, disk, description string
		system                  bool
	}{
		{"rejects root disk", "nvme0n1", "root disk", true},
		{"rejects USB SSD", "sdd", "USB SSD", false},
		{"rejects swap disk", "sde", "swap disk", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBackendFixture(t)
			all, err := b.list(context.Background())
			requireNoError(t, err)
			got := requireIndexedDevice(t, indexDevicesByName(all), tc.disk)
			if tc.system {
				assertSystemDisk(t, got, tc.description)
			} else {
				assertRejected(t, got, tc.description)
			}
		})
	}
}

func assertMountedFlashMetadata(t *testing.T, flash device.Device) {
	t.Helper()
	if !flash.Mounted {
		t.Fatalf("flash not mounted: %+v", flash)
	}
	if flash.PartitionCount != 2 {
		t.Fatalf("flash partition count = %d, want 2: %+v", flash.PartitionCount, flash)
	}
	if len(flash.MountPoints) != 2 {
		t.Fatalf("flash mount points = %q, want two: %+v", flash.MountPoints, flash)
	}
	if flash.MountPoints[0] != "/media/My USB" || flash.MountPoints[1] != "/media/Backup" {
		t.Fatalf("flash mount points = %q, want [/media/My USB /media/Backup]", flash.MountPoints)
	}
}

func TestSmallGenericUSBStorageFallback(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sectors uint64
		allowed bool
	}{
		{name: "allows a small removable disk", sectors: 65536, allowed: true},
		{name: "rejects a disk over 128 GB", sectors: maxGenericUSBFlashSize/512 + 1, allowed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBackendFixture(t)
			b.useGenericUSBStorage("sdb", "FLASH123")
			b.setDiskSectors("sdb", tc.sectors)
			gotDevice, err := b.RefreshDevice(context.Background(), "FLASH123")
			requireNoError(t, err)
			if gotDevice.IsAllowed != tc.allowed {
				t.Fatalf("allowed = %t, want %t: %+v", gotDevice.IsAllowed, tc.allowed, gotDevice)
			}
		})
	}
}

func requireDevicePaths(t *testing.T, devices []device.Device, paths ...string) {
	t.Helper()
	if len(devices) != len(paths) {
		t.Fatalf("allowed devices = %d: %+v", len(devices), devices)
	}
	found := make(map[string]bool, len(devices))
	for _, d := range devices {
		found[filepath.Base(d.Path)] = true
	}
	for _, path := range paths {
		if !found[path] {
			t.Fatalf("device %q missing from %+v", path, devices)
		}
	}
}

func indexDevicesByName(devices []device.Device) map[string]device.Device {
	indexed := make(map[string]device.Device, len(devices))
	for _, d := range devices {
		indexed[filepath.Base(d.Path)] = d
	}
	return indexed
}

func requireIndexedDevice(t *testing.T, devices map[string]device.Device, name string) device.Device {
	t.Helper()
	d, ok := devices[name]
	if !ok {
		t.Fatalf("device %q was not enumerated", name)
	}
	return d
}

func assertSystemDisk(t *testing.T, d device.Device, description string) {
	t.Helper()
	if !d.IsSystemDisk || d.IsAllowed {
		t.Fatalf("%s was not rejected as a system disk: %+v", description, d)
	}
}

func assertRejected(t *testing.T, d device.Device, description string) {
	t.Helper()
	if d.IsAllowed {
		t.Fatalf("%s was allowed: %+v", description, d)
	}
}

func TestEnumerationFailsClosedWithoutSwapMetadata(t *testing.T) {
	b := newBackendFixture(t)
	b.removeSwapMetadata()
	if _, err := b.ListAllowedDevices(context.Background()); err == nil {
		t.Fatal("enumeration succeeded without swap metadata")
	}
}
