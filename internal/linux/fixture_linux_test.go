//go:build linux

package linux

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func requireSameStrings(t *testing.T, description string, got, want []string) {
	t.Helper()
	got = slices.Clone(got)
	want = slices.Clone(want)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", description, got, want)
	}
}

type backendFixture struct {
	*Backend
	t *testing.T
}

func newBackendFixture(t *testing.T) *backendFixture {
	t.Helper()
	root := t.TempDir()
	class := filepath.Join(root, "sys/class/block")
	devices := filepath.Join(root, "sys/devices")
	dev := filepath.Join(root, "dev")
	udevData := filepath.Join(root, "run/udev/data")
	for _, path := range []string{class, devices, dev, udevData} {
		requireNoError(t, os.MkdirAll(path, 0755))
	}
	addFixtureDevices(t, class, devices, dev)
	mount := filepath.Join(root, "mountinfo")
	writeMountInfo(t, mount)
	swaps := filepath.Join(root, "swaps")
	write(t, swaps, "Filename Type Size Used Priority\n"+filepath.Join(dev, "sde1")+" partition 1024 0 -2\n")
	b := &Backend{SysClassBlock: class, MountInfo: mount, Swaps: swaps, DevRoot: dev, UdevDataRoot: udevData, udisks: &fakeUDisks{mountInfo: mount}}
	setUdevProperties(t, b, "sdb", "ID_BUS=usb\nID_SERIAL_SHORT=FLASH123\nID_DRIVE_THUMB=1\n")
	setUdevProperties(t, b, "sdc", "ID_BUS=usb\nID_SERIAL_SHORT=CARD123\nID_DRIVE_FLASH_SD=1\n")
	setUdevProperties(t, b, "sdd", "ID_BUS=usb\nID_SERIAL_SHORT=SSD123\nID_DRIVE_THUMB=1\nID_ATA=1\n")
	setUdevProperties(t, b, "sde", "ID_BUS=usb\nID_SERIAL_SHORT=SWAP123\nID_DRIVE_THUMB=1\n")
	setUdevProperties(t, b, "nvme0n1", "ID_BUS=nvme\nID_SERIAL_SHORT=SYS123\n")
	return &backendFixture{Backend: b, t: t}
}

func (f *backendFixture) useGenericUSBStorage(name, serial string) {
	f.setUdevProperties(name, "ID_BUS=usb\nID_SERIAL_SHORT="+serial+"\nID_USB_DRIVER=usb-storage\n")
}

func (f *backendFixture) replaceIdentity(name, serial, kindProperty string) {
	f.setUdevProperties(name, "ID_BUS=usb\nID_SERIAL_SHORT="+serial+"\n"+kindProperty+"=1\n")
}

func (f *backendFixture) setDiskSectors(name string, sectors uint64) {
	real, err := filepath.EvalSymlinks(filepath.Join(f.SysClassBlock, name))
	requireNoError(f.t, err)
	write(f.t, filepath.Join(real, "size"), fmt.Sprint(sectors))
}

func (f *backendFixture) removeSwapMetadata() {
	f.Swaps = filepath.Join(f.t.TempDir(), "missing-swaps")
}

func (f *backendFixture) setUdevProperties(name, properties string) {
	setUdevProperties(f.t, f.Backend, name, properties)
}

func setUdevProperties(t *testing.T, b *Backend, name, properties string) {
	t.Helper()
	deviceNumber := readTrim(filepath.Join(b.SysClassBlock, name, "dev"))
	lines := strings.Split(strings.TrimSpace(properties), "\n")
	for i := range lines {
		lines[i] = "E:" + lines[i]
	}
	write(t, filepath.Join(b.UdevDataRoot, "b"+deviceNumber), strings.Join(lines, "\n")+"\n")
}

func addFixtureDevices(t *testing.T, class, devices, dev string) {
	t.Helper()
	paths := fixturePaths{class: class, devices: devices, dev: dev}
	addFixtureDisk(t, paths, fixtureDisk{name: "sdb", devno: "8:16", serial: "FLASH123", usb: true, removable: true, model: "Thumb Drive"})
	addFixturePartition(t, paths, fixturePartition{parent: "sdb", name: "sdb1", devno: "8:17"})
	addFixturePartition(t, paths, fixturePartition{parent: "sdb", name: "sdb2", devno: "8:18"})
	addFixtureDisk(t, paths, fixtureDisk{name: "sdc", devno: "8:32", serial: "CARD123", usb: true, removable: true, model: "Card Reader"})
	addFixtureDisk(t, paths, fixtureDisk{name: "sdd", devno: "8:48", serial: "SSD123", usb: true, removable: true, model: "Portable SSD"})
	addFixtureDisk(t, paths, fixtureDisk{name: "sde", devno: "8:64", serial: "SWAP123", usb: true, removable: true, model: "Thumb Drive"})
	addFixturePartition(t, paths, fixturePartition{parent: "sde", name: "sde1", devno: "8:65"})
	addFixtureDisk(t, paths, fixtureDisk{name: "nvme0n1", devno: "259:0", serial: "SYS123", model: "Internal"})
	addFixturePartition(t, paths, fixturePartition{parent: "nvme0n1", name: "nvme0n1p1", devno: "259:1"})
}

type fixturePaths struct {
	class   string
	devices string
	dev     string
}

type fixtureDisk struct {
	name      string
	devno     string
	serial    string
	model     string
	usb       bool
	removable bool
}

func addFixtureDisk(t *testing.T, paths fixturePaths, disk fixtureDisk) {
	t.Helper()
	base := filepath.Join(paths.devices, "pci0000:00")
	if disk.usb {
		base = filepath.Join(base, "usb1", "1-1")
	}
	base = filepath.Join(base, "block", disk.name)
	requireNoError(t, os.MkdirAll(filepath.Join(base, "device"), 0755))
	for path, value := range map[string]string{"dev": disk.devno, "size": "65536", "removable": boolDigit(disk.removable), "device/type": "0", "device/vendor": "Acme", "device/model": disk.model, "device/serial": disk.serial} {
		write(t, filepath.Join(base, path), value)
	}
	requireNoError(t, os.Symlink(base, filepath.Join(paths.class, disk.name)))
	requireNoError(t, os.WriteFile(filepath.Join(paths.dev, disk.name), nil, 0600))
}

type fixturePartition struct {
	parent string
	name   string
	devno  string
}

func addFixturePartition(t *testing.T, paths fixturePaths, partition fixturePartition) {
	t.Helper()
	parentReal, err := filepath.EvalSymlinks(filepath.Join(paths.class, partition.parent))
	requireNoError(t, err)
	base := filepath.Join(parentReal, partition.name)
	requireNoError(t, os.MkdirAll(base, 0755))
	write(t, filepath.Join(base, "dev"), partition.devno)
	write(t, filepath.Join(base, "partition"), "1")
	requireNoError(t, os.Symlink(base, filepath.Join(paths.class, partition.name)))
	requireNoError(t, os.WriteFile(filepath.Join(paths.dev, partition.name), nil, 0600))
}
func writeMountInfo(t *testing.T, path string) {
	t.Helper()
	write(t, path, "42 1 8:17 / /media/My\\040USB rw - vfat /dev/sdb1 rw\n43 1 8:18 / /media/Backup rw - vfat /dev/sdb2 rw\n44 1 259:1 / / rw - ext4 /dev/nvme0n1p1 rw\n")
}
func write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
func boolDigit(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
