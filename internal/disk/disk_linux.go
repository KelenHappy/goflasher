//go:build linux

package disk

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/goflasher/goflasher/internal/udisks"
)

type linuxManager struct {
	sysClassBlock blockClassDir
	mountInfo     mountInfoFile
	swaps         swapsFile
	devRoot       string
	udisks        udisks.Client
}

type linuxDiskState struct {
	mounts mountTable
	swaps  swapSet
}

// NewManager returns the native Linux implementation without exposing it in
// the common API.
func NewManager() Manager {
	return &linuxManager{
		sysClassBlock: "/sys/class/block",
		mountInfo:     "/proc/self/mountinfo",
		swaps:         "/proc/swaps",
		devRoot:       "/dev",
		udisks:        udisks.New(),
	}
}

// deviceNumber is a block device's major:minor pair.
type deviceNumber struct{ major, minor uint64 }

func (n deviceNumber) String() string { return fmt.Sprintf("%d:%d", n.major, n.minor) }

func parseDeviceNumber(value string) (deviceNumber, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return deviceNumber{}, false
	}
	major, err1 := strconv.ParseUint(parts[0], 10, 32)
	minor, err2 := strconv.ParseUint(parts[1], 10, 32)
	return deviceNumber{major, minor}, err1 == nil && err2 == nil
}

// blockName is a kernel block device name such as sdb or sdb1.
type blockName string

// sysfsAttr is a file inside a block device's sysfs directory.
type sysfsAttr string

const (
	attrDev       sysfsAttr = "dev"
	attrPartition sysfsAttr = "partition"
	attrSize      sysfsAttr = "size"
	attrRemovable sysfsAttr = "removable"
	attrVendor    sysfsAttr = "device/vendor"
	attrModel     sysfsAttr = "device/model"
	attrSerial    sysfsAttr = "device/serial"
)

// blockClassDir is the /sys/class/block directory, which links every disk
// and partition name to its sysfs device directory.
type blockClassDir string

func (d blockClassDir) entry(name blockName) blockClass {
	return blockClass(filepath.Join(string(d), string(name)))
}

// parentOf names the whole disk that owns the device number: the device
// itself when it is not a partition.
func (d blockClassDir) parentOf(number deviceNumber) blockName {
	entries, err := os.ReadDir(string(d))
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		name := blockName(entry.Name())
		class := d.entry(name)
		if !class.hasNumber(number) {
			continue
		}
		if !class.isPartition() {
			return name
		}
		if parent, ok := class.parent(); ok {
			return parent
		}
	}
	return ""
}

// wholeDisk names the disk that name belongs to: name itself for a disk, or
// the disk a partition sits under.
func (d blockClassDir) wholeDisk(name blockName) blockName {
	class := d.entry(name)
	if !class.exists() {
		return ""
	}
	if !class.isPartition() {
		return name
	}
	parent, _ := class.parent()
	return parent
}

// blockClass is one /sys/class/block entry.
type blockClass string

func (c blockClass) attrPath(a sysfsAttr) string { return filepath.Join(string(c), string(a)) }
func (c blockClass) attr(a sysfsAttr) string     { return readText(c.attrPath(a)) }
func (c blockClass) uintAttr(a sysfsAttr) uint64 {
	value, _ := strconv.ParseUint(c.attr(a), 10, 64)
	return value
}
func (c blockClass) exists() bool      { return pathExists(string(c)) }
func (c blockClass) isPartition() bool { return pathExists(c.attrPath(attrPartition)) }

func (c blockClass) deviceNumber() (deviceNumber, bool) { return parseDeviceNumber(c.attr(attrDev)) }

// hasNumber reports whether the entry's dev file holds the given major:minor pair.
func (c blockClass) hasNumber(number deviceNumber) bool {
	n, ok := c.deviceNumber()
	return ok && n == number
}

// resolve follows the class symlink to the physical sysfs device directory.
func (c blockClass) resolve() (string, error) { return filepath.EvalSymlinks(string(c)) }

// parent names the disk whose sysfs directory holds this partition.
func (c blockClass) parent() (blockName, bool) {
	real, err := c.resolve()
	if err != nil {
		return "", false
	}
	return blockName(filepath.Base(filepath.Dir(real))), true
}

// blockEntry is a whole-disk class entry with its symlink resolved.
type blockEntry struct {
	name  blockName
	class blockClass
	real  string
}

// entryOutcome separates entries that were never disks from disks that could
// not be read. Only the latter is reported: partitions are filtered on every
// healthy system and would drown the count in noise.
type entryOutcome int

const (
	entryDisk entryOutcome = iota
	entryNotADisk
	entrySkipped
)

func (m *linuxManager) List(ctx context.Context) ([]Disk, int, error) {
	entries, err := os.ReadDir(string(m.sysClassBlock))
	if err != nil {
		return nil, 0, err
	}
	mounts, err := m.mountInfo.mounts()
	if err != nil {
		return nil, 0, err
	}
	swaps, err := m.swaps.devices()
	if err != nil {
		return nil, 0, err
	}
	state := linuxDiskState{mounts: mounts, swaps: swaps}
	result := make([]Disk, 0, len(entries))
	skipped := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		disk, outcome := m.diskFromEntry(blockName(entry.Name()), state)
		switch outcome {
		case entryDisk:
			result = append(result, disk)
		case entrySkipped:
			skipped++
		}
	}
	return result, skipped, nil
}

func (m *linuxManager) diskFromEntry(name blockName, state linuxDiskState) (Disk, entryOutcome) {
	class := m.sysClassBlock.entry(name)
	if class.isPartition() {
		return Disk{}, entryNotADisk
	}
	real, err := class.resolve()
	if err != nil {
		return Disk{}, entrySkipped
	}
	number, ok := class.deviceNumber()
	if !ok {
		return Disk{}, entrySkipped
	}
	disk := m.readDisk(blockEntry{name: name, class: class, real: real})
	disk.MountPoints = m.diskMountPoints(name, number, state.mounts)
	disk.Mounted = len(disk.MountPoints) != 0
	disk.System = systemMountPointPresent(disk.MountPoints) || m.diskUsedForSwap(name, state.swaps)
	if disk.ID == "" {
		disk.ID = fmt.Sprintf("%s@%s", number, real)
	}
	return disk, entryDisk
}

func (m *linuxManager) readDisk(e blockEntry) Disk {
	serial := e.class.attr(attrSerial)
	disk := Disk{Device: m.devicePath(e.name), Vendor: e.class.attr(attrVendor), Model: e.class.attr(attrModel), Serial: serial, ID: serial, Size: e.class.uintAttr(attrSize) * 512, Removable: e.class.attr(attrRemovable) == "1", External: strings.Contains(e.real, "/usb")}
	if disk.External {
		disk.Bus = "usb"
	}
	return disk
}

func (m *linuxManager) devicePath(name blockName) string {
	return filepath.Join(m.devRoot, string(name))
}

// diskMountPoints collects the mount points of the disk and of its partitions,
// sorted and without duplicates.
func (m *linuxManager) diskMountPoints(name blockName, number deviceNumber, mounts mountTable) []string {
	var result []string
	for n, points := range mounts {
		if n == number || m.sysClassBlock.parentOf(n) == name {
			result = append(result, points...)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func systemMountPointPresent(points []string) bool {
	for _, point := range points {
		switch point {
		case "/", "/boot", "/boot/efi", "/home":
			return true
		}
	}
	return false
}

func (m *linuxManager) diskUsedForSwap(name blockName, swaps swapSet) bool {
	for swap := range swaps {
		if m.sysClassBlock.wholeDisk(blockName(filepath.Base(swap))) == name {
			return true
		}
	}
	return false
}

func (m *linuxManager) Refresh(ctx context.Context, id string) (Disk, error) {
	disks, _, err := m.List(ctx)
	if err != nil {
		return Disk{}, err
	}
	for _, candidate := range disks {
		if candidate.ID == id {
			return candidate, nil
		}
	}
	return Disk{}, ErrNotFound
}

func (m *linuxManager) Unmount(ctx context.Context, selected Disk) error {
	fresh, err := m.revalidateForDestructiveOperation(ctx, selected)
	if err != nil {
		return err
	}
	if err := m.unmountPoints(ctx, fresh.MountPoints); err != nil {
		return err
	}
	return m.verifyUnmounted(ctx, fresh)
}

func (m *linuxManager) unmountPoints(ctx context.Context, points []string) error {
	for _, point := range points {
		source := m.mountInfo.sourceOf(point)
		if source == "" {
			return fmt.Errorf("%w: mount source for %s not found", ErrUnmountFailed, point)
		}
		if err := m.diskService().Unmount(ctx, source); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrUnmountFailed, source, err)
		}
	}
	return nil
}

func (m *linuxManager) verifyUnmounted(ctx context.Context, disk Disk) error {
	again, err := m.Refresh(ctx, disk.ID)
	if err != nil {
		return fmt.Errorf("%w: refresh after unmount: %v", ErrUnmountFailed, err)
	}
	if again.Mounted {
		return fmt.Errorf("%w: device remains mounted", ErrUnmountFailed)
	}
	return nil
}

func (m *linuxManager) revalidateForDestructiveOperation(ctx context.Context, selected Disk) (Disk, error) {
	fresh, err := m.revalidate(ctx, selected)
	if err != nil {
		return Disk{}, err
	}
	if fresh.System {
		return Disk{}, ErrSystemDisk
	}
	if !fresh.External || !fresh.Removable {
		return Disk{}, ErrNotRemovable
	}
	return fresh, nil
}

func (m *linuxManager) Eject(ctx context.Context, selected Disk) error {
	fresh, err := m.revalidate(ctx, selected)
	if err != nil {
		return err
	}
	if fresh.System {
		return ErrSystemDisk
	}
	if !fresh.External || !fresh.Removable {
		return ErrNotRemovable
	}
	if fresh.Mounted {
		return ErrUnmountFailed
	}
	if err := m.diskService().PowerOff(ctx, fresh.Device); err != nil {
		return fmt.Errorf("%w: %v", ErrEjectFailed, err)
	}
	return nil
}

func (m *linuxManager) diskService() udisks.Client {
	if m.udisks == nil {
		m.udisks = udisks.New()
	}
	return m.udisks
}

func (m *linuxManager) revalidate(ctx context.Context, selected Disk) (Disk, error) {
	fresh, err := m.Refresh(ctx, selected.ID)
	if err != nil {
		return Disk{}, fmt.Errorf("%w: %v", ErrChanged, err)
	}
	if !SameIdentity(selected, fresh) {
		return Disk{}, ErrChanged
	}
	return fresh, nil
}

// mountInfoFile is a file in /proc/self/mountinfo format.
type mountInfoFile string

// mountTable lists the mount points of each mounted device number.
type mountTable map[deviceNumber][]string

// mountInfoLine is the whitespace-separated fields of one mountinfo line.
type mountInfoLine []string

// lines returns every line read before the end of the file or the first error.
func (f mountInfoFile) lines() ([]mountInfoLine, error) {
	file, err := os.Open(string(f))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var lines []mountInfoLine
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, strings.Fields(scanner.Text()))
	}
	return lines, scanner.Err()
}

func (f mountInfoFile) mounts() (mountTable, error) {
	lines, err := f.lines()
	if err != nil {
		return nil, err
	}
	result := make(mountTable)
	for _, l := range lines {
		if number, ok := l.deviceNumber(); ok {
			result[number] = append(result[number], l.mountPoint())
		}
	}
	return result, nil
}

// sourceOf returns the device mounted at point, or "" when none is found.
func (f mountInfoFile) sourceOf(point string) string {
	lines, _ := f.lines()
	for _, l := range lines {
		if source, ok := l.sourceFor(point); ok {
			return source
		}
	}
	return ""
}

func (l mountInfoLine) mountPoint() string { return l[4] }

func (l mountInfoLine) deviceNumber() (deviceNumber, bool) {
	if len(l) < 5 {
		return deviceNumber{}, false
	}
	return parseDeviceNumber(l[2])
}

// sourceFor returns the mount source when the line mounts point. The source
// follows the filesystem type after the "-" separator.
func (l mountInfoLine) sourceFor(point string) (string, bool) {
	if len(l) < 10 || l.mountPoint() != point {
		return "", false
	}
	separator := slices.Index(l, "-")
	if separator < 0 || separator+2 >= len(l) {
		return "", false
	}
	return l[separator+2], true
}

// swapsFile is a file in /proc/swaps format: a header line, then one swap
// device path per line.
type swapsFile string

// swapSet holds the device paths in use as swap.
type swapSet map[string]bool

func (f swapsFile) devices() (swapSet, error) {
	result := make(swapSet)
	if f == "" {
		return result, nil
	}
	file, err := os.Open(string(f))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) != 0 {
			result[fields[0]] = true
		}
	}
	return result, scanner.Err()
}

func readText(path string) string {
	data, _ := os.ReadFile(path)
	return strings.TrimSpace(string(data))
}
func pathExists(path string) bool { _, err := os.Stat(path); return err == nil }
