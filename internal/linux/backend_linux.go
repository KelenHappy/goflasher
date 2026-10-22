//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/progress"
	"github.com/goflasher/goflasher/internal/udisks"
)

var (
	ErrUnsupportedDevice = errors.New("unsupported device")
	ErrSystemDisk        = errors.New("system disk")
	ErrDeviceChanged     = errors.New("device identity changed")
	ErrUnmountFailed     = errors.New("unmount failed")
	// ErrDeviceBusy reports that the privileged helper could not claim the
	// disk exclusively because something else, such as a mount, holds it.
	ErrDeviceBusy = errors.New("device is busy")
	// ErrUnsupportedSectorSize reports a device whose logical block size differs
	// from the sector size the requested on-disk layout is built for.
	ErrUnsupportedSectorSize = errors.New("unsupported logical sector size")
)

// maxGenericUSBFlashSize is a conservative fallback for removable USB mass
// storage that udev does not tag with ID_DRIVE_THUMB or ID_DRIVE_FLASH.  Use
// decimal GB here to match the capacity printed on consumer flash drives.
const maxGenericUSBFlashSize uint64 = 128_000_000_000

// Backend enumerates Linux block devices from sysfs. udev is supplementary:
// candidates must satisfy kernel topology and conservative udev classification.
type Backend struct {
	SysClassBlock string
	MountInfo     string
	Swaps         string
	DevRoot       string
	UdevDataRoot  string
	helper        privilegedHelper
	udisks        udisks.Client
}

var _ device.Backend = (*Backend)(nil)
var _ device.WindowsInstallerBackend = (*Backend)(nil)
var _ device.HotplugBackend = (*Backend)(nil)

func NewBackend() *Backend {
	return &Backend{SysClassBlock: "/sys/class/block", MountInfo: "/proc/self/mountinfo", Swaps: "/proc/swaps", DevRoot: "/dev", UdevDataRoot: "/run/udev/data", helper: newCommandHelper(), udisks: udisks.New()}
}

func (b *Backend) ListAllowedDevices(ctx context.Context) ([]device.Device, device.ScanReport, error) {
	all, skipped, err := b.list(ctx)
	if err != nil {
		return nil, device.ScanReport{}, err
	}
	allowed := make([]device.Device, 0, len(all))
	for _, d := range all {
		if d.IsAllowed {
			allowed = append(allowed, d)
		}
	}
	return allowed, device.ScanReport{Skipped: skipped}, nil
}

// list returns every enumerated device plus the number of whole disks that
// were present but could not be inspected.
func (b *Backend) list(ctx context.Context) ([]device.Device, int, error) {
	snapshot, err := b.enumerationSnapshot()
	if err != nil {
		return nil, 0, err
	}
	result := make([]device.Device, 0, len(snapshot.entries))
	skipped := 0
	for _, entry := range snapshot.entries {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		d, outcome, err := b.deviceFromEntry(entry, snapshot)
		if err != nil {
			return nil, 0, err
		}
		switch outcome {
		case entryDevice:
			result = append(result, d)
		case entrySkipped:
			skipped++
		}
	}
	return result, skipped, nil
}

type enumerationSnapshot struct {
	entries  []os.DirEntry
	mounts   mountTable
	swaps    swapTable
	topology *blockTopology
}

func (b *Backend) enumerationSnapshot() (enumerationSnapshot, error) {
	entries, err := os.ReadDir(b.SysClassBlock)
	if err != nil {
		return enumerationSnapshot{}, err
	}
	mounts, err := parseMountInfo(b.MountInfo)
	if err != nil {
		return enumerationSnapshot{}, err
	}
	swaps, err := parseSwaps(b.Swaps)
	if err != nil {
		return enumerationSnapshot{}, err
	}
	topology, err := readBlockTopology(b.SysClassBlock)
	if err != nil {
		return enumerationSnapshot{}, err
	}
	return enumerationSnapshot{entries: entries, mounts: mounts, swaps: swaps, topology: topology}, nil
}

// entryOutcome distinguishes a block entry that was never a candidate from one
// that was, but could not be inspected. Only the latter is worth reporting to
// the user: partitions are filtered structurally on every healthy system.
type entryOutcome int

const (
	// entryDevice means the entry yielded a usable device.
	entryDevice entryOutcome = iota
	// entryNotADisk means the entry is a partition, never a whole-disk target.
	entryNotADisk
	// entrySkipped means the entry is a whole disk whose sysfs identity could
	// not be read, so it was omitted from the listing. readBlockTopology
	// already fails the whole scan for an unreadable entry, so this is reached
	// only when a device disappears between that snapshot and this inspection.
	entrySkipped
)

func (b *Backend) deviceFromEntry(entry os.DirEntry, snapshot enumerationSnapshot) (device.Device, entryOutcome, error) {
	disk := blockDevice{name: entry.Name(), link: filepath.Join(b.SysClassBlock, entry.Name())}
	if disk.isPartition() {
		return device.Device{}, entryNotADisk, nil
	}
	real, err := filepath.EvalSymlinks(disk.link)
	if err != nil {
		return device.Device{}, entrySkipped, nil
	}
	disk.real = sysfsPath(real)
	if disk.number, err = readDeviceNumber(filepath.Join(disk.link, "dev")); err != nil {
		return device.Device{}, entrySkipped, nil
	}
	candidate := sysfsDevice{blockDevice: disk, properties: b.udev(disk.number)}
	d := b.basicDevice(candidate)
	d.PartitionCount = snapshot.partitionCount(disk)
	if err := b.populateSafetyMetadata(&d, candidate, snapshot); err != nil {
		return device.Device{}, entrySkipped, err
	}
	b.classifyDevice(&d, candidate)
	d.ID = first(d.Serial, d.WWN, disk.topologyID())
	return d, entryDevice, nil
}

// sysfsDevice is a whole-disk candidate with its supplementary udev record.
type sysfsDevice struct {
	blockDevice
	properties udevProperties
}

func (b *Backend) basicDevice(candidate sysfsDevice) device.Device {
	return device.Device{
		Path: filepath.Join(b.DevRoot, candidate.name), Major: candidate.number.major, Minor: candidate.number.minor, SysfsPath: string(candidate.real),
		Vendor: candidate.attr("device/vendor"), Model: candidate.attr("device/model"),
		Serial: first(candidate.properties["ID_SERIAL_SHORT"], candidate.attr("device/serial")),
		WWN:    candidate.properties["ID_WWN"], Transport: strings.ToLower(candidate.properties["ID_BUS"]), Size: candidate.capacity(),
	}
}

func (b *Backend) populateSafetyMetadata(d *device.Device, candidate sysfsDevice, snapshot enumerationSnapshot) error {
	mountPoints, err := snapshot.mounts.pointsBackedBy(candidate.blockDevice, snapshot.topology)
	if err != nil {
		return err
	}
	hasSwap, err := snapshot.swaps.backs(candidate.blockDevice, b.DevRoot, snapshot.topology)
	if err != nil {
		return err
	}
	d.MountPoints = mountPoints
	d.Mounted = len(mountPoints) > 0
	d.IsSystemDisk = containsCriticalMount(mountPoints) || hasSwap
	return nil
}

func (s enumerationSnapshot) partitionCount(disk blockDevice) int {
	root := filepath.Dir(disk.link)
	n := 0
	for _, e := range s.entries {
		if strings.HasPrefix(e.Name(), disk.name) && exists(filepath.Join(root, e.Name(), "partition")) {
			n++
		}
	}
	return n
}

func (b *Backend) classifyDevice(d *device.Device, candidate sysfsDevice) {
	d.IsCardReader = candidate.properties.isCardReader()
	c := characterize(*d, candidate)
	switch {
	case d.IsSystemDisk:
		d.RejectReason = ErrSystemDisk.Error()
	case c.unsupported():
		d.RejectReason = ErrUnsupportedDevice.Error()
	case !c.positivelyIdentified():
		d.RejectReason = "not positively identified as USB flash media"
	default:
		d.IsAllowed = true
	}
}

// udevProperties is one device's E: records from the udev database.
type udevProperties map[string]string

func (p udevProperties) isFlash() bool {
	if p["ID_DRIVE_THUMB"] == "1" {
		return true
	}
	return p["ID_DRIVE_FLASH"] == "1"
}

func (p udevProperties) isCardReader() bool {
	for _, k := range []string{"ID_DRIVE_FLASH_SD", "ID_DRIVE_FLASH_MMC", "ID_DRIVE_FLASH_CF", "ID_DRIVE_FLASH_MS", "ID_DRIVE_FLASH_SM"} {
		if p[k] == "1" {
			return true
		}
	}
	return false
}

func (p udevProperties) isSmallUSBStorage(size uint64) bool {
	if p["ID_USB_DRIVER"] != "usb-storage" {
		return false
	}
	return size <= maxGenericUSBFlashSize
}

func (p udevProperties) suspicious(d device.Device) bool {
	description := strings.ToLower(strings.Join([]string{d.Vendor, d.Model, p["ID_MODEL"], p["ID_VENDOR"]}, " "))
	if containsAny(description, "ssd", "hard disk", "hdd", "bridge", "sata", "nvme") {
		return true
	}
	if p["ID_ATA"] == "1" {
		return true
	}
	return p["ID_USB_DRIVER"] == "uas"
}

type deviceCharacteristics struct {
	usb             bool
	removable       bool
	diskType        bool
	size            uint64
	suspicious      bool
	flash           bool
	cardReader      bool
	smallUSBStorage bool
}

func characterize(d device.Device, candidate sysfsDevice) deviceCharacteristics {
	return deviceCharacteristics{
		usb:             d.Transport == "usb" && candidate.real.underUSB(),
		removable:       candidate.attr("removable") == "1",
		diskType:        candidate.attr("device/type") == "0",
		size:            d.Size,
		suspicious:      candidate.properties.suspicious(d),
		flash:           candidate.properties.isFlash(),
		cardReader:      d.IsCardReader,
		smallUSBStorage: candidate.properties.isSmallUSBStorage(d.Size),
	}
}

func (c deviceCharacteristics) unsupported() bool {
	return !c.usb ||
		!c.removable ||
		!c.diskType ||
		c.size == 0 ||
		c.suspicious
}

func (c deviceCharacteristics) positivelyIdentified() bool {
	if c.flash {
		return true
	}
	if c.cardReader {
		return true
	}
	return c.smallUSBStorage
}

func (b *Backend) RefreshDevice(ctx context.Context, id string) (device.Device, error) {
	all, _, err := b.list(ctx)
	if err != nil {
		return device.Device{}, err
	}
	for _, d := range all {
		if d.ID == id {
			return d, nil
		}
	}
	return device.Device{}, os.ErrNotExist
}
func (b *Backend) Revalidate(ctx context.Context, selected device.Device) (device.Device, error) {
	fresh, err := b.RefreshDevice(ctx, selected.ID)
	if err != nil {
		return device.Device{}, fmt.Errorf("%w: %w", ErrDeviceChanged, err)
	}
	if !fresh.IsAllowed {
		return device.Device{}, fmt.Errorf("%w: %s", ErrUnsupportedDevice, fresh.RejectReason)
	}
	if !device.SameIdentity(selected, fresh) {
		return device.Device{}, ErrDeviceChanged
	}
	return fresh, nil
}

func (b *Backend) Unmount(ctx context.Context, d device.Device) error {
	fresh, err := b.Revalidate(ctx, d)
	if err != nil {
		return err
	}
	for _, partition := range b.mountedPartitions(fresh) {
		if err := b.diskService().Unmount(ctx, partition); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrUnmountFailed, partition, err)
		}
	}
	again, err := b.Revalidate(ctx, fresh)
	if err != nil {
		return err
	}
	if again.Mounted {
		return fmt.Errorf("%w: device remains mounted", ErrUnmountFailed)
	}
	return nil
}

func (b *Backend) OpenWriter(ctx context.Context, d device.Device) (io.WriteCloser, error) {
	fresh, err := b.Revalidate(ctx, d)
	if err != nil {
		return nil, err
	}
	if fresh.Mounted {
		return nil, ErrUnmountFailed
	}
	return b.privileged().OpenWriter(ctx, helperRequest(fresh, modeWrite))
}
func (b *Backend) OpenReader(ctx context.Context, d device.Device) (io.ReadCloser, error) {
	fresh, err := b.Revalidate(ctx, d)
	if err != nil {
		return nil, err
	}
	if fresh.Mounted {
		return nil, ErrUnmountFailed
	}
	return b.privileged().OpenReader(ctx, helperRequest(fresh, modeRead))
}
func (b *Backend) OpenInstallerTarget(ctx context.Context, d device.Device) (device.InstallerTarget, error) {
	session, err := b.openInstallerSession(ctx, d)
	if err != nil {
		return nil, err
	}
	return session, nil
}
func (b *Backend) OpenInstallerReader(ctx context.Context, d device.Device) (device.InstallerReader, error) {
	session, err := b.openInstallerSession(ctx, d)
	if err != nil {
		return nil, err
	}
	return session, nil
}
func (b *Backend) openInstallerSession(ctx context.Context, d device.Device) (*remoteInstallerSession, error) {
	fresh, err := b.Revalidate(ctx, d)
	if err != nil {
		return nil, err
	}
	if fresh.Mounted {
		return nil, ErrUnmountFailed
	}
	helper, ok := b.privileged().(installerSessionHelper)
	if !ok {
		return nil, ErrUnsupportedDevice
	}
	request := helperRequest(fresh, modeInstallerSession)
	request.LogicalSectorSize = 512
	return helper.OpenInstallerSession(ctx, request)
}
func (b *Backend) Flush(ctx context.Context, d device.Device) error {
	fresh, err := b.Revalidate(ctx, d)
	if err != nil {
		return err
	}
	if fresh.Mounted {
		return ErrUnmountFailed
	}
	return b.privileged().Flush(ctx, helperRequest(fresh, modeFlush))
}

func (b *Backend) privileged() privilegedHelper {
	if b.helper == nil {
		b.helper = newCommandHelper()
	}
	return b.helper
}
func (b *Backend) Eject(ctx context.Context, d device.Device) error {
	fresh, err := b.Revalidate(ctx, d)
	if err != nil {
		return err
	}
	return b.diskService().PowerOff(ctx, fresh.Path)
}

// WatchDevices follows UDisks2's device objects, which it adds and removes as
// udev reports block devices coming and going.
func (b *Backend) WatchDevices(ctx context.Context, changed func()) error {
	return b.diskService().Watch(ctx, changed)
}

func (b *Backend) diskService() udisks.Client {
	if b.udisks == nil {
		b.udisks = udisks.New()
	}
	return b.udisks
}

// FormatFAT32 creates a FAT32 filesystem on the whole removable device through
// the narrowly scoped privileged helper; revalidation and unmounting happen
// first.
func (b *Backend) FormatFAT32(ctx context.Context, d device.Device, label string, updates chan<- progress.Update) error {
	fresh, err := b.Revalidate(ctx, d)
	if err != nil {
		return err
	}
	// Best-effort unmount: corrupted partition tables may leave phantom
	// partitions that UDisks2 cannot unmount. The privileged helper
	// re-validates identity and opens the whole-disk device directly, so
	// a failed unmount of a damaged partition does not block formatting.
	_ = b.Unmount(ctx, fresh)
	again, err := b.Revalidate(ctx, fresh)
	if err != nil {
		return err
	}
	request := helperRequest(again, modeFormatFAT32)
	request.Label = label
	if err := b.privileged().FormatFAT32(ctx, request, updates); err != nil {
		return fmt.Errorf("format FAT32: %w", err)
	}
	return nil
}

func readTrim(p string) string { v, _ := os.ReadFile(p); return strings.TrimSpace(string(v)) }
func readUint(p string) uint64 { v, _ := strconv.ParseUint(readTrim(p), 10, 64); return v }
func exists(p string) bool     { _, e := os.Stat(p); return e == nil }
func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
func containsAny(s string, values ...string) bool {
	for _, v := range values {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}
func appendUnique(dst []string, values ...string) []string {
	for _, value := range values {
		found := false
		for _, existing := range dst {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, value)
		}
	}
	return dst
}
func parentForPath(root, path string) string {
	name := filepath.Base(path)
	real, e := filepath.EvalSymlinks(filepath.Join(root, name))
	if e != nil {
		return name
	}
	if exists(filepath.Join(root, name, "partition")) {
		return filepath.Base(filepath.Dir(real))
	}
	return name
}
func mustReadDir(p string) []os.DirEntry { e, _ := os.ReadDir(p); return e }
func (b *Backend) mountedPartitions(parent device.Device) []string {
	mounts, _ := parseMountInfo(b.MountInfo)
	var result []string
	for number := range mounts {
		for _, entry := range mustReadDir(b.SysClassBlock) {
			if readTrim(filepath.Join(b.SysClassBlock, entry.Name(), "dev")) == number.String() && parentForPath(b.SysClassBlock, entry.Name()) == filepath.Base(parent.Path) {
				result = append(result, filepath.Join(b.DevRoot, entry.Name()))
			}
		}
	}
	return result
}
func (b *Backend) udev(number devNumber) udevProperties {
	data, err := os.ReadFile(filepath.Join(b.UdevDataRoot, "b"+number.String()))
	if err != nil {
		return udevProperties{}
	}
	properties := udevProperties{}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "E:") {
			continue
		}
		if key, value, ok := strings.Cut(strings.TrimPrefix(line, "E:"), "="); ok {
			properties[key] = value
		}
	}
	return properties
}
