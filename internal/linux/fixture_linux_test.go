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
	paths := fixturePaths{
		class:    filepath.Join(root, "sys/class/block"),
		devices:  filepath.Join(root, "sys/devices"),
		dev:      filepath.Join(root, "dev"),
		udevData: filepath.Join(root, "run/udev/data"),
	}
	for _, path := range []string{paths.class, paths.devices, paths.dev, paths.udevData} {
		requireNoError(t, os.MkdirAll(path, 0755))
	}
	addFixtureDevices(t, paths)
	mount := filepath.Join(root, "mountinfo")
	write(t, mount, "42 1 8:17 / /media/My\\040USB rw - vfat /dev/sdb1 rw\n43 1 8:18 / /media/Backup rw - vfat /dev/sdb2 rw\n44 1 259:1 / / rw - ext4 /dev/nvme0n1p1 rw\n")
	swaps := filepath.Join(root, "swaps")
	write(t, swaps, "Filename Type Size Used Priority\n"+filepath.Join(paths.dev, "sde1")+" partition 1024 0 -2\n")
	b := &Backend{SysClassBlock: paths.class, MountInfo: mount, Swaps: swaps, DevRoot: paths.dev, UdevDataRoot: paths.udevData, udisks: &fakeUDisks{mountInfo: mount}}
	return &backendFixture{Backend: b, t: t}
}

// udevIdentity is the udev database entry of a fixture disk. Each flag is a
// classification property such as ID_DRIVE_THUMB set to 1; driver, when set,
// becomes ID_USB_DRIVER.
type udevIdentity struct {
	bus    string
	serial string
	flags  []string
	driver string
}

// usbIdentity is a USB disk that udev classifies through flags.
func usbIdentity(serial string, flags ...string) udevIdentity {
	return udevIdentity{bus: "usb", serial: serial, flags: flags}
}

// genericUSBStorage is a USB disk that udev only knows as usb-storage, without
// a drive classification.
func genericUSBStorage(serial string) udevIdentity {
	return udevIdentity{bus: "usb", serial: serial, driver: "usb-storage"}
}

// database renders the identity in the format of /run/udev/data/b<major>:<minor>.
func (id udevIdentity) database() string {
	lines := []string{"E:ID_BUS=" + id.bus, "E:ID_SERIAL_SHORT=" + id.serial}
	for _, flag := range id.flags {
		lines = append(lines, "E:"+flag+"=1")
	}
	if id.driver != "" {
		lines = append(lines, "E:ID_USB_DRIVER="+id.driver)
	}
	return strings.Join(lines, "\n") + "\n"
}

// setUdevIdentity replaces the udev entry of the named fixture disk.
func (f *backendFixture) setUdevIdentity(name string, id udevIdentity) {
	deviceNumber := readTrim(filepath.Join(f.SysClassBlock, name, "dev"))
	write(f.t, filepath.Join(f.UdevDataRoot, "b"+deviceNumber), id.database())
}

func (f *backendFixture) setDiskSectors(name string, sectors uint64) {
	real, err := filepath.EvalSymlinks(filepath.Join(f.SysClassBlock, name))
	requireNoError(f.t, err)
	write(f.t, filepath.Join(real, "size"), fmt.Sprint(sectors))
}

func (f *backendFixture) removeSwapMetadata() {
	f.Swaps = filepath.Join(f.t.TempDir(), "missing-swaps")
}

func addFixtureDevices(t *testing.T, paths fixturePaths) {
	t.Helper()
	addFixtureDisk(t, paths, fixtureDisk{name: "sdb", devno: "8:16", usb: true, removable: true, model: "Thumb Drive", udev: usbIdentity("FLASH123", "ID_DRIVE_THUMB")})
	addFixturePartition(t, paths, fixturePartition{parent: "sdb", name: "sdb1", devno: "8:17"})
	addFixturePartition(t, paths, fixturePartition{parent: "sdb", name: "sdb2", devno: "8:18"})
	addFixtureDisk(t, paths, fixtureDisk{name: "sdc", devno: "8:32", usb: true, removable: true, model: "Card Reader", udev: usbIdentity("CARD123", "ID_DRIVE_FLASH_SD")})
	addFixtureDisk(t, paths, fixtureDisk{name: "sdd", devno: "8:48", usb: true, removable: true, model: "Portable SSD", udev: usbIdentity("SSD123", "ID_DRIVE_THUMB", "ID_ATA")})
	addFixtureDisk(t, paths, fixtureDisk{name: "sde", devno: "8:64", usb: true, removable: true, model: "Thumb Drive", udev: usbIdentity("SWAP123", "ID_DRIVE_THUMB")})
	addFixturePartition(t, paths, fixturePartition{parent: "sde", name: "sde1", devno: "8:65"})
	addFixtureDisk(t, paths, fixtureDisk{name: "nvme0n1", devno: "259:0", model: "Internal", udev: udevIdentity{bus: "nvme", serial: "SYS123"}})
	addFixturePartition(t, paths, fixturePartition{parent: "nvme0n1", name: "nvme0n1p1", devno: "259:1"})
}

type fixturePaths struct {
	class    string
	devices  string
	dev      string
	udevData string
}

// fixtureDisk is one whole disk; its sysfs serial is the udev short serial.
type fixtureDisk struct {
	name      string
	devno     string
	model     string
	usb       bool
	removable bool
	udev      udevIdentity
}

func addFixtureDisk(t *testing.T, paths fixturePaths, disk fixtureDisk) {
	t.Helper()
	base := filepath.Join(paths.devices, "pci0000:00")
	if disk.usb {
		base = filepath.Join(base, "usb1", "1-1")
	}
	base = filepath.Join(base, "block", disk.name)
	requireNoError(t, os.MkdirAll(filepath.Join(base, "device"), 0755))
	for path, value := range map[string]string{"dev": disk.devno, "size": "65536", "removable": boolDigit(disk.removable), "queue/logical_block_size": "512", "device/type": "0", "device/vendor": "Acme", "device/model": disk.model, "device/serial": disk.udev.serial} {
		write(t, filepath.Join(base, path), value)
	}
	requireNoError(t, os.Symlink(base, filepath.Join(paths.class, disk.name)))
	requireNoError(t, os.WriteFile(filepath.Join(paths.dev, disk.name), nil, 0600))
	write(t, filepath.Join(paths.udevData, "b"+disk.devno), disk.udev.database())
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
