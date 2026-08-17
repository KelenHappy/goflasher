//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func addFixtureStackDevice(t *testing.T, b *Backend, name, devno string) {
	t.Helper()
	root := filepath.Dir(filepath.Dir(b.SysClassBlock))
	base := filepath.Join(root, "devices", "virtual", "block", name)
	requireNoError(t, os.MkdirAll(filepath.Join(base, "slaves"), 0755))
	requireNoError(t, os.MkdirAll(filepath.Join(base, "holders"), 0755))
	write(t, filepath.Join(base, "dev"), devno)
	requireNoError(t, os.Symlink(base, filepath.Join(b.SysClassBlock, name)))
}

func addFixtureDependency(t *testing.T, b *Backend, dependant, dependency string) {
	t.Helper()
	dependantReal, err := filepath.EvalSymlinks(filepath.Join(b.SysClassBlock, dependant))
	requireNoError(t, err)
	dependencyReal, err := filepath.EvalSymlinks(filepath.Join(b.SysClassBlock, dependency))
	requireNoError(t, err)
	requireNoError(t, os.MkdirAll(filepath.Join(dependantReal, "slaves"), 0755))
	requireNoError(t, os.MkdirAll(filepath.Join(dependencyReal, "holders"), 0755))
	requireNoError(t, os.Symlink(dependencyReal, filepath.Join(dependantReal, "slaves", dependency)))
	requireNoError(t, os.Symlink(dependantReal, filepath.Join(dependencyReal, "holders", dependant)))
}

func setFixtureMount(t *testing.T, b *Backend, devno, source, point string) {
	t.Helper()
	write(t, b.MountInfo, fmt.Sprintf("42 1 %s / %s rw - ext4 %s rw\n", devno, point, source))
}

func clearFixtureActivity(t *testing.T, b *Backend) {
	t.Helper()
	write(t, b.MountInfo, "")
	write(t, b.Swaps, "Filename Type Size Used Priority\n")
}

func TestPlainMountedPartitionIsDetectedAtBothBoundaries(t *testing.T) {
	f := newBackendFixture(t)
	got, err := f.RefreshDevice(context.Background(), "FLASH123")
	requireNoError(t, err)
	if !got.Mounted {
		t.Fatal("backend did not associate directly mounted partition with disk")
	}
	unsafe, err := mountedOrSystem("sdb", 8, 16, helperEnvironment{SysClassBlock: f.SysClassBlock, MountInfo: f.MountInfo, Swaps: f.Swaps, DevRoot: f.DevRoot})
	requireNoError(t, err)
	if !unsafe {
		t.Fatal("privileged helper accepted disk with a mounted partition")
	}
}

func TestStackedMountedDevicesRejectBackingUSB(t *testing.T) {
	for _, tc := range []struct {
		name       string
		point      string
		stack      []string
		useHolders bool
	}{
		{name: "LUKS device-mapper root", point: "/", stack: []string{"cryptroot"}},
		{name: "LVM logical volume", point: "/home", stack: []string{"dm-crypt", "dm-lvm"}},
		{name: "md and device-mapper", point: "/boot", stack: []string{"md0", "dm-boot"}, useHolders: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBackendFixture(t)
			clearFixtureActivity(t, f.Backend)
			dependency := "sdb2"
			for i, name := range tc.stack {
				addFixtureStackDevice(t, f.Backend, name, fmt.Sprintf("253:%d", i))
				addFixtureDependency(t, f.Backend, name, dependency)
				if tc.useHolders {
					real, err := filepath.EvalSymlinks(filepath.Join(f.SysClassBlock, name))
					requireNoError(t, err)
					requireNoError(t, os.Remove(filepath.Join(real, "slaves", dependency)))
				}
				dependency = name
			}
			setFixtureMount(t, f.Backend, fmt.Sprintf("253:%d", len(tc.stack)-1), "/dev/"+dependency, tc.point)

			got, err := f.RefreshDevice(context.Background(), "FLASH123")
			requireNoError(t, err)
			assertSystemDisk(t, got, tc.name)
			unsafe, err := mountedOrSystem("sdb", 8, 16, helperEnvironment{SysClassBlock: f.SysClassBlock, MountInfo: f.MountInfo, Swaps: f.Swaps, DevRoot: f.DevRoot})
			requireNoError(t, err)
			if !unsafe {
				t.Fatal("privileged helper safety check accepted stacked system disk")
			}
		})
	}
}

func TestStackedSwapRejectsBackingUSBInBothBoundaries(t *testing.T) {
	f := newBackendFixture(t)
	clearFixtureActivity(t, f.Backend)
	addFixtureStackDevice(t, f.Backend, "dm-swap", "253:7")
	addFixtureDependency(t, f.Backend, "dm-swap", "sdb1")
	write(t, f.Swaps, "Filename Type Size Used Priority\n"+filepath.Join(f.DevRoot, "dm-swap")+" partition 1024 0 -2\n")

	got, err := f.RefreshDevice(context.Background(), "FLASH123")
	requireNoError(t, err)
	assertSystemDisk(t, got, "stacked swap")
	unsafe, err := mountedOrSystem("sdb", 8, 16, helperEnvironment{SysClassBlock: f.SysClassBlock, MountInfo: f.MountInfo, Swaps: f.Swaps, DevRoot: f.DevRoot})
	requireNoError(t, err)
	if !unsafe {
		t.Fatal("privileged helper safety check accepted stacked swap disk")
	}
}

func TestUnrelatedDeviceMapperDoesNotRejectUSB(t *testing.T) {
	f := newBackendFixture(t)
	clearFixtureActivity(t, f.Backend)
	addFixtureStackDevice(t, f.Backend, "dm-root", "253:0")
	addFixtureDependency(t, f.Backend, "dm-root", "nvme0n1p1")
	setFixtureMount(t, f.Backend, "253:0", "/dev/dm-root", "/")

	got, err := f.RefreshDevice(context.Background(), "FLASH123")
	requireNoError(t, err)
	if !got.IsAllowed {
		t.Fatalf("unrelated USB was rejected: %+v", got)
	}
}

func TestMalformedOrCyclicTopologyFailsClosed(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		f := newBackendFixture(t)
		clearFixtureActivity(t, f.Backend)
		addFixtureStackDevice(t, f.Backend, "dm-root", "253:0")
		addFixtureDependency(t, f.Backend, "dm-root", "sdb2")
		setFixtureMount(t, f.Backend, "253:0", "/dev/dm-root", "/")
		if malformed {
			real, err := filepath.EvalSymlinks(filepath.Join(f.SysClassBlock, "dm-root"))
			requireNoError(t, err)
			requireNoError(t, os.Symlink(filepath.Join(f.SysClassBlock, "missing"), filepath.Join(real, "slaves", "missing")))
		} else {
			addFixtureDependency(t, f.Backend, "sdb2", "dm-root")
		}
		if _, err := f.ListAllowedDevices(context.Background()); err == nil {
			t.Fatal("backend accepted unsafe topology")
		}
		_, err := mountedOrSystem("sdb", 8, 16, helperEnvironment{SysClassBlock: f.SysClassBlock, MountInfo: f.MountInfo, Swaps: f.Swaps, DevRoot: f.DevRoot})
		if err == nil || errors.Is(err, ErrSystemDisk) {
			t.Fatalf("privileged topology validation did not fail closed: %v", err)
		}
	}
}
