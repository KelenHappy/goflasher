//go:build linux

package disk

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/goflasher/goflasher/internal/udisks"
)

type linuxManager struct {
	sysClassBlock string
	mountInfo     string
	swaps         string
	devRoot       string
	udisks        udisks.Client
}

type linuxDiskState struct {
	mounts map[[2]uint64][]string
	swaps  map[string]bool
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
	entries, err := os.ReadDir(m.sysClassBlock)
	if err != nil {
		return nil, 0, err
	}
	mounts, err := linuxMounts(m.mountInfo)
	if err != nil {
		return nil, 0, err
	}
	swaps, err := linuxSwaps(m.swaps)
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
		disk, outcome := m.diskFromEntry(entry.Name(), state)
		switch outcome {
		case entryDisk:
			result = append(result, disk)
		case entrySkipped:
			skipped++
		}
	}
	return result, skipped, nil
}

func (m *linuxManager) diskFromEntry(name string, state linuxDiskState) (Disk, entryOutcome) {
	class := filepath.Join(m.sysClassBlock, name)
	if pathExists(filepath.Join(class, "partition")) {
		return Disk{}, entryNotADisk
	}
	real, err := filepath.EvalSymlinks(class)
	if err != nil {
		return Disk{}, entrySkipped
	}
	major, minor, ok := linuxDeviceNumber(readText(filepath.Join(class, "dev")))
	if !ok {
		return Disk{}, entrySkipped
	}
	disk := m.readDisk(class, name, real)
	disk.MountPoints = m.diskMountPoints(name, [2]uint64{major, minor}, state.mounts)
	disk.Mounted = len(disk.MountPoints) != 0
	disk.System = systemMountPointPresent(disk.MountPoints) || m.diskUsedForSwap(name, state.swaps)
	if disk.ID == "" {
		disk.ID = fmt.Sprintf("%d:%d@%s", major, minor, real)
	}
	return disk, entryDisk
}

func (m *linuxManager) readDisk(class, name, real string) Disk {
	serial := readText(filepath.Join(class, "device/serial"))
	disk := Disk{Device: filepath.Join(m.devRoot, name), Vendor: readText(filepath.Join(class, "device/vendor")), Model: readText(filepath.Join(class, "device/model")), Serial: serial, ID: serial, Size: readUint(filepath.Join(class, "size")) * 512, Removable: readText(filepath.Join(class, "removable")) == "1", External: strings.Contains(real, "/usb")}
	if disk.External {
		disk.Bus = "usb"
	}
	return disk
}

func (m *linuxManager) diskMountPoints(name string, deviceNumber [2]uint64, mounts map[[2]uint64][]string) []string {
	var result []string
	for number, points := range mounts {
		if number != deviceNumber && linuxParent(m.sysClassBlock, number) != name {
			continue
		}
		for _, point := range points {
			result = appendUnique(result, point)
		}
	}
	return result
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

func (m *linuxManager) diskUsedForSwap(name string, swaps map[string]bool) bool {
	for swap := range swaps {
		if linuxWholeName(m.sysClassBlock, filepath.Base(swap)) == name {
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
	return m.verifyUnmounted(ctx, fresh.ID)
}

func (m *linuxManager) unmountPoints(ctx context.Context, points []string) error {
	for _, point := range points {
		source := linuxMountSource(m.mountInfo, point)
		if source == "" {
			return fmt.Errorf("%w: mount source for %s not found", ErrUnmountFailed, point)
		}
		if err := m.diskService().Unmount(ctx, source); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrUnmountFailed, source, err)
		}
	}
	return nil
}

func (m *linuxManager) verifyUnmounted(ctx context.Context, id string) error {
	again, err := m.Refresh(ctx, id)
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

func linuxMounts(path string) (map[[2]uint64][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	result := make(map[[2]uint64][]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		major, minor, ok := linuxDeviceNumber(fields[2])
		if ok {
			result[[2]uint64{major, minor}] = append(result[[2]uint64{major, minor}], fields[4])
		}
	}
	return result, scanner.Err()
}

func linuxSwaps(path string) (map[string]bool, error) {
	result := make(map[string]bool)
	if path == "" {
		return result, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
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

func linuxMountSource(path, point string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if source, ok := mountSourceForPoint(strings.Fields(scanner.Text()), point); ok {
			return source
		}
	}
	return ""
}

func mountSourceForPoint(fields []string, point string) (string, bool) {
	if len(fields) < 10 || fields[4] != point {
		return "", false
	}
	separator := indexOf(fields, "-")
	if separator < 0 || separator+2 >= len(fields) {
		return "", false
	}
	return fields[separator+2], true
}

func indexOf(values []string, target string) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}

func linuxDeviceNumber(value string) (uint64, uint64, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	major, err1 := strconv.ParseUint(parts[0], 10, 32)
	minor, err2 := strconv.ParseUint(parts[1], 10, 32)
	return major, minor, err1 == nil && err2 == nil
}

func linuxParent(root string, number [2]uint64) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		class := filepath.Join(root, entry.Name())
		if !linuxClassHasNumber(class, number) {
			continue
		}
		if !pathExists(filepath.Join(class, "partition")) {
			return entry.Name()
		}
		real, err := filepath.EvalSymlinks(class)
		if err == nil {
			return filepath.Base(filepath.Dir(real))
		}
	}
	return ""
}

// linuxClassHasNumber reports whether the sysfs block class entry's dev file
// holds the given major:minor pair.
func linuxClassHasNumber(class string, number [2]uint64) bool {
	major, minor, ok := linuxDeviceNumber(readText(filepath.Join(class, "dev")))
	return ok && [2]uint64{major, minor} == number
}

func linuxWholeName(root, name string) string {
	class := filepath.Join(root, name)
	if !pathExists(class) {
		return ""
	}
	if !pathExists(filepath.Join(class, "partition")) {
		return name
	}
	real, err := filepath.EvalSymlinks(class)
	if err != nil {
		return ""
	}
	return filepath.Base(filepath.Dir(real))
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func readText(path string) string {
	data, _ := os.ReadFile(path)
	return strings.TrimSpace(string(data))
}
func readUint(path string) uint64 {
	value, _ := strconv.ParseUint(readText(path), 10, 64)
	return value
}
func pathExists(path string) bool { _, err := os.Stat(path); return err == nil }
