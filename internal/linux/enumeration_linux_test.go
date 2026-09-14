//go:build linux

package linux

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/goflasher/goflasher/internal/device"
)

func TestEnumerationFiltersAndMounts(t *testing.T) {
	t.Run("returns only supported removable devices", func(t *testing.T) {
		b := newBackendFixture(t)
		devices, _, err := b.ListAllowedDevices(context.Background())
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
			all, _, err := b.list(context.Background())
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
	requireSameStrings(t, "flash mount points", flash.MountPoints, []string{"/media/My USB", "/media/Backup"})
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
	if _, _, err := b.ListAllowedDevices(context.Background()); err == nil {
		t.Fatal("enumeration succeeded without swap metadata")
	}
}

// Linux fails the whole scan closed when any block entry is unreadable
// (readBlockTopology), so the skipped path is reachable only if an entry
// disappears between that snapshot and its own inspection. The count must
// therefore stay zero on a healthy tree: partitions must never inflate it.
func TestEnumerationReportsNoSkipsOnHealthyTree(t *testing.T) {
	b := newBackendFixture(t)
	_, skipped, err := b.list(context.Background())
	requireNoError(t, err)
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0; partitions must not be counted", skipped)
	}
	_, report, err := b.ListAllowedDevices(context.Background())
	requireNoError(t, err)
	if report.Skipped != 0 {
		t.Fatalf("report.Skipped = %d, want 0", report.Skipped)
	}
}

// An entry that vanishes after the topology snapshot is taken must be counted,
// not silently dropped.
func TestEnumerationCountsEntryThatVanishesMidScan(t *testing.T) {
	b := newBackendFixture(t)
	dir := t.TempDir()
	requireNoError(t, os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "sdz")))
	entries, err := os.ReadDir(dir)
	requireNoError(t, err)
	d, outcome, err := b.deviceFromEntry(entries[0], enumerationSnapshot{})
	requireNoError(t, err)
	if outcome != entrySkipped {
		t.Fatalf("outcome = %v, want entrySkipped (device %+v)", outcome, d)
	}
}
