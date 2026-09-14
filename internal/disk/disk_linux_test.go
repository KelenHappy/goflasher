//go:build linux

package disk

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fakeUDisks struct {
	unmounted  []string
	poweredOff []string
	mountInfo  string
	err        error
}

func (f *fakeUDisks) Unmount(_ context.Context, device string) error {
	f.unmounted = append(f.unmounted, device)
	if f.err != nil {
		return f.err
	}
	return os.WriteFile(f.mountInfo, nil, 0600)
}

func (f *fakeUDisks) PowerOff(_ context.Context, device string) error {
	f.poweredOff = append(f.poweredOff, device)
	return f.err
}

// sysfsFixture builds a fake sysfs tree for a single removable USB disk
// named sdb and returns the class/block directory plus the physical path.
type sysfsFixture struct {
	root      string
	class     string
	physical  string
	mountInfo string
}

func newSysfsFixture(t *testing.T) sysfsFixture {
	t.Helper()
	root := t.TempDir()
	f := sysfsFixture{
		root:      root,
		class:     filepath.Join(root, "sys", "class", "block"),
		physical:  filepath.Join(root, "sys", "devices", "pci", "usb1", "block", "sdb"),
		mountInfo: filepath.Join(root, "mountinfo"),
	}
	mustMkdirAll(t, f.class, filepath.Join(f.physical, "device"))
	mustWriteFiles(t, f.physical, map[string]string{
		"dev": "8:16\n", "size": "2048\n", "removable": "1\n",
		"device/vendor": "Example\n", "device/model": "Flash\n", "device/serial": "SERIAL\n",
	})
	mustSymlink(t, f.physical, filepath.Join(f.class, "sdb"))
	f.setMountInfo(t, "")
	return f
}

// addPartition registers sdb1 with the given device number under the disk.
func (f sysfsFixture) addPartition(t *testing.T, number string) {
	t.Helper()
	partition := filepath.Join(f.physical, "sdb1")
	mustMkdirAll(t, partition)
	mustWriteFiles(t, partition, map[string]string{"dev": number + "\n", "partition": "1\n"})
	mustSymlink(t, partition, filepath.Join(f.class, "sdb1"))
}

func (f sysfsFixture) setMountInfo(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(f.mountInfo, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func (f sysfsFixture) manager(udisks *fakeUDisks) *linuxManager {
	m := &linuxManager{sysClassBlock: f.class, mountInfo: f.mountInfo, devRoot: "/dev"}
	if udisks != nil {
		m.udisks = udisks
	}
	return m
}

func mustMkdirAll(t *testing.T, directories ...string) {
	t.Helper()
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
	}
}

func mustWriteFiles(t *testing.T, base string, files map[string]string) {
	t.Helper()
	for path, value := range files {
		if err := os.WriteFile(filepath.Join(base, path), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func listSingleDisk(t *testing.T, m *linuxManager) Disk {
	t.Helper()
	disks, _, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 1 {
		t.Fatalf("disk count = %d, want 1", len(disks))
	}
	return disks[0]
}

func TestLinuxManagerListsWholeDiskFromSysfs(t *testing.T) {
	fixture := newSysfsFixture(t)
	got := listSingleDisk(t, fixture.manager(nil))

	want := Disk{ID: "SERIAL", Device: "/dev/sdb", Size: 2048 * 512, Removable: true, External: true, Bus: "usb"}
	checks := []struct {
		field     string
		got, want any
	}{
		{"ID", got.ID, want.ID},
		{"Device", got.Device, want.Device},
		{"Size", got.Size, want.Size},
		{"Removable", got.Removable, want.Removable},
		{"External", got.External, want.External},
		{"Bus", got.Bus, want.Bus},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}
}

func TestLinuxManagerUnmountsPartitionAndRechecksState(t *testing.T) {
	fixture := newSysfsFixture(t)
	fixture.addPartition(t, "8:17")
	fixture.setMountInfo(t, "36 25 8:17 / /media/usb rw - vfat /dev/sdb1 rw\n")
	service := &fakeUDisks{mountInfo: fixture.mountInfo}
	m := fixture.manager(service)

	disk := listSingleDisk(t, m)
	if !disk.Mounted {
		t.Fatalf("disk not reported as mounted: %+v", disk)
	}
	if err := m.Unmount(context.Background(), disk); err != nil {
		t.Fatal(err)
	}
	if len(service.unmounted) != 1 {
		t.Fatalf("unmounted devices = %q, want exactly one", service.unmounted)
	}
	if service.unmounted[0] != "/dev/sdb1" {
		t.Fatalf("unmounted device = %q, want /dev/sdb1", service.unmounted[0])
	}
}

func TestLinuxDeviceNumber(t *testing.T) {
	tests := []struct {
		input        string
		major, minor uint64
		ok           bool
	}{
		{input: "259:12\n", major: 259, minor: 12, ok: true},
		{input: "invalid"},
	}
	for _, tc := range tests {
		major, minor, ok := linuxDeviceNumber(tc.input)
		if ok != tc.ok {
			t.Errorf("linuxDeviceNumber(%q) ok = %v, want %v", tc.input, ok, tc.ok)
			continue
		}
		if major != tc.major {
			t.Errorf("linuxDeviceNumber(%q) major = %d, want %d", tc.input, major, tc.major)
		}
		if minor != tc.minor {
			t.Errorf("linuxDeviceNumber(%q) minor = %d, want %d", tc.input, minor, tc.minor)
		}
	}
}
