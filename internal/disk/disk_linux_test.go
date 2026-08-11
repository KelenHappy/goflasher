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

func TestLinuxManagerListsWholeDiskFromSysfs(t *testing.T) {
	root := t.TempDir()
	class := filepath.Join(root, "sys", "class", "block")
	physical := filepath.Join(root, "sys", "devices", "pci", "usb1", "block", "sdb")
	if err := os.MkdirAll(filepath.Join(physical, "device"), 0755); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{
		"dev": "8:16\n", "size": "2048\n", "removable": "1\n",
		"device/vendor": "Example\n", "device/model": "Flash\n", "device/serial": "SERIAL\n",
	} {
		if err := os.WriteFile(filepath.Join(physical, path), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(class, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, filepath.Join(class, "sdb")); err != nil {
		t.Fatal(err)
	}
	mountInfo := filepath.Join(root, "mountinfo")
	if err := os.WriteFile(mountInfo, nil, 0600); err != nil {
		t.Fatal(err)
	}
	m := &linuxManager{sysClassBlock: class, mountInfo: mountInfo, devRoot: "/dev"}
	disks, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 1 {
		t.Fatalf("disk count = %d, want 1", len(disks))
	}
	got := disks[0]
	if got.ID != "SERIAL" || got.Device != "/dev/sdb" || got.Size != 2048*512 || !got.Removable || !got.External || got.Bus != "usb" {
		t.Fatalf("unexpected disk: %+v", got)
	}
}

func TestLinuxManagerUnmountsPartitionAndRechecksState(t *testing.T) {
	root := t.TempDir()
	class := filepath.Join(root, "class")
	physical := filepath.Join(root, "devices", "usb1", "block", "sdb")
	partition := filepath.Join(physical, "sdb1")
	for _, directory := range []string{filepath.Join(physical, "device"), partition, class} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for path, value := range map[string]string{
		filepath.Join(physical, "dev"): "8:16\n", filepath.Join(physical, "size"): "2048\n",
		filepath.Join(physical, "removable"): "1\n", filepath.Join(physical, "device/serial"): "SERIAL\n",
		filepath.Join(partition, "dev"): "8:17\n", filepath.Join(partition, "partition"): "1\n",
	} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(physical, filepath.Join(class, "sdb")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(partition, filepath.Join(class, "sdb1")); err != nil {
		t.Fatal(err)
	}
	mountInfo := filepath.Join(root, "mountinfo")
	if err := os.WriteFile(mountInfo, []byte("36 25 8:17 / /media/usb rw - vfat /dev/sdb1 rw\n"), 0600); err != nil {
		t.Fatal(err)
	}
	service := &fakeUDisks{mountInfo: mountInfo}
	m := &linuxManager{
		sysClassBlock: class, mountInfo: mountInfo, devRoot: "/dev",
		udisks: service,
	}
	disks, err := m.List(context.Background())
	if err != nil || len(disks) != 1 || !disks[0].Mounted {
		t.Fatalf("List() = %+v, %v", disks, err)
	}
	if err := m.Unmount(context.Background(), disks[0]); err != nil {
		t.Fatal(err)
	}
	if len(service.unmounted) != 1 || service.unmounted[0] != "/dev/sdb1" {
		t.Fatalf("unmounted devices = %q, want /dev/sdb1", service.unmounted)
	}
}

func TestLinuxDeviceNumber(t *testing.T) {
	major, minor, ok := linuxDeviceNumber("259:12\n")
	if !ok || major != 259 || minor != 12 {
		t.Fatalf("linuxDeviceNumber = %d:%d, %v", major, minor, ok)
	}
	if _, _, ok := linuxDeviceNumber("invalid"); ok {
		t.Fatal("invalid device number accepted")
	}
}
