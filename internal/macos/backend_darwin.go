//go:build darwin

// Package macos is the migration writer implementation. Platform discovery,
// identity, refresh, unmount and eject policy is owned exclusively by disk.Manager.
package macos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/disk"
	"github.com/goflasher/goflasher/internal/fat32"
	"github.com/goflasher/goflasher/internal/progress"
)

var (
	ErrUnsupportedDevice = disk.ErrNotRemovable
	ErrSystemDisk        = disk.ErrSystemDisk
	ErrDeviceChanged     = disk.ErrChanged
	ErrUnmountFailed     = disk.ErrUnmountFailed
)

const formatTimeout = 10 * time.Minute

type formatTarget interface {
	fat32.Device
	io.Closer
	Fd() uintptr
}
type backend struct {
	manager    disk.Manager
	openRaw    func(string, int) (*os.File, error)
	openFormat func(string) (formatTarget, error)
	fstat      func(int, *syscall.Stat_t) error
	stat       func(string, *syscall.Stat_t) error
}

var (
	_ device.Backend                 = (*backend)(nil)
	_ device.WindowsInstallerBackend = (*backend)(nil)
)

func NewBackend() *backend { return NewBackendWithManager(disk.NewManager()) }
func NewBackendWithManager(manager disk.Manager) *backend {
	return &backend{manager: manager}
}

// exclusiveLockFlags requests a non-blocking advisory exclusive open lock for
// writes. O_EXLOCK coordinates with DiskArbitration and Apple's own disk tools
// (newfs, diskutil, hdiutil) so they do not auto-mount or write metadata to the
// just-unmounted device mid-operation; O_NONBLOCK makes the open fail fast with
// EAGAIN instead of blocking (os.OpenFile ignores context, so a blocking open
// would defeat formatTimeout), which we surface as ErrDeviceBusy. The lock is
// advisory — it coordinates with cooperating openers, it is NOT a security
// boundary. The authoritative post-open check remains validateOpenedDisk.
const exclusiveLockFlags = syscall.O_EXLOCK | syscall.O_NONBLOCK

// ErrDeviceBusy reports that the raw device could not be opened exclusively
// because another process holds it.
var ErrDeviceBusy = errors.New("device is busy")

func openLocked(path string, flag int) (*os.File, error) {
	f, err := os.OpenFile(path, flag, 0)
	if err != nil && errors.Is(err, syscall.EAGAIN) {
		return nil, fmt.Errorf("%w: %w", ErrDeviceBusy, err)
	}
	return f, err
}

func deviceFromDisk(d disk.Disk) device.Device {
	n, _ := wholeDiskNumber(strings.TrimPrefix(d.Device, "/dev/r"))
	return device.Device{ID: d.ID, Path: d.Device, Vendor: d.Vendor, Model: d.Model, Serial: d.Serial, Transport: d.Bus, SysfsPath: d.RegistryPath, Major: n, Size: d.Size, IsCardReader: d.Ejectable, Mounted: d.Mounted, IsSystemDisk: d.System, IsAllowed: d.Removable && d.External && !d.System && d.Bus == "usb" && d.Size > 0 && d.RegistryID != "", MountPoints: append([]string(nil), d.MountPoints...), PartitionCount: len(d.MountPoints)}
}
func (b *backend) ListAllowedDevices(ctx context.Context) ([]device.Device, error) {
	all, err := b.manager.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]device.Device, 0, len(all))
	for _, d := range all {
		v := deviceFromDisk(d)
		if v.IsAllowed {
			out = append(out, v)
		}
	}
	return out, nil
}
func (b *backend) RefreshDevice(ctx context.Context, id string) (device.Device, error) {
	d, err := b.manager.Refresh(ctx, id)
	if err != nil {
		return device.Device{}, err
	}
	v := deviceFromDisk(d)
	if !v.IsAllowed {
		return device.Device{}, ErrUnsupportedDevice
	}
	return v, nil
}
func (b *backend) diskSnapshot(ctx context.Context, selected device.Device) (disk.Disk, error) {
	d, err := b.manager.Refresh(ctx, selected.ID)
	if err != nil {
		return disk.Disk{}, fmt.Errorf("%w: %w", ErrDeviceChanged, err)
	}
	fresh := deviceFromDisk(d)
	if !fresh.IsAllowed {
		return disk.Disk{}, ErrUnsupportedDevice
	}
	if !device.SameIdentity(selected, fresh) {
		return disk.Disk{}, ErrDeviceChanged
	}
	if selected.Size != fresh.Size {
		return disk.Disk{}, ErrDeviceChanged
	}
	if selected.Model != fresh.Model {
		return disk.Disk{}, ErrDeviceChanged
	}
	return d, nil
}
func (b *backend) Unmount(ctx context.Context, selected device.Device) error {
	d, err := b.diskSnapshot(ctx, selected)
	if err != nil {
		return err
	}
	return b.manager.Unmount(ctx, d)
}
func (b *backend) open(ctx context.Context, selected device.Device, flag int) (*os.File, error) {
	d, err := b.diskSnapshot(ctx, selected)
	if err != nil {
		return nil, err
	}
	if d.Mounted {
		return nil, ErrUnmountFailed
	}
	opener := b.openRaw
	if opener == nil {
		// Only writers need exclusivity; read-back opens must not contend with
		// the preceding write or format on the same device.
		if flag&syscall.O_ACCMODE != os.O_RDONLY {
			flag |= exclusiveLockFlags
		}
		opener = openLocked
	}
	f, err := opener(d.Device, flag|syscall.O_CLOEXEC)
	if err != nil {
		return nil, err
	}
	if err := b.validateOpenedDisk(ctx, d, f.Fd()); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// validateOpenedDisk ties the refreshed IOKit identity to the kernel object
// referenced by fd. Raw Darwin disk nodes are character devices; matching the
// fd's rdev to the freshly authorized BSD node prevents path replacement with
// a different device number between refresh and open.
func (b *backend) validateOpenedDisk(ctx context.Context, authorized disk.Disk, fd uintptr) error {
	opened, err := b.openedDiskStat(fd)
	if err != nil {
		return ErrDeviceChanged
	}
	fresh, err := b.refreshedAuthorizedDisk(ctx, authorized)
	if err != nil {
		return ErrDeviceChanged
	}
	if !b.matchesDeviceNode(fresh.Device, opened.Rdev) {
		return ErrDeviceChanged
	}
	return nil
}

func (b *backend) openedDiskStat(fd uintptr) (syscall.Stat_t, error) {
	fstat := b.fstat
	if fstat == nil {
		fstat = syscall.Fstat
	}
	var opened syscall.Stat_t
	if err := fstat(int(fd), &opened); err != nil {
		return syscall.Stat_t{}, err
	}
	if opened.Mode&syscall.S_IFMT != syscall.S_IFCHR {
		return syscall.Stat_t{}, ErrDeviceChanged
	}
	return opened, nil
}

func (b *backend) refreshedAuthorizedDisk(ctx context.Context, authorized disk.Disk) (disk.Disk, error) {
	fresh, err := b.manager.Refresh(ctx, authorized.ID)
	if err != nil {
		return disk.Disk{}, err
	}
	if fresh.Mounted {
		return disk.Disk{}, ErrDeviceChanged
	}
	if !deviceFromDisk(fresh).IsAllowed {
		return disk.Disk{}, ErrDeviceChanged
	}
	if !disk.SameIdentity(authorized, fresh) {
		return disk.Disk{}, ErrDeviceChanged
	}
	return fresh, nil
}

func (b *backend) matchesDeviceNode(path string, rdev int32) bool {
	stat := b.stat
	if stat == nil {
		stat = syscall.Stat
	}
	var node syscall.Stat_t
	if err := stat(path, &node); err != nil {
		return false
	}
	if node.Mode&syscall.S_IFMT != syscall.S_IFCHR {
		return false
	}
	return node.Rdev == rdev
}
func (b *backend) OpenWriter(ctx context.Context, d device.Device) (io.WriteCloser, error) {
	return b.open(ctx, d, os.O_WRONLY)
}
func (b *backend) OpenReader(ctx context.Context, d device.Device) (io.ReadCloser, error) {
	return b.open(ctx, d, os.O_RDONLY)
}

// OpenInstallerTarget returns the same identity-bound, exclusively opened raw
// descriptor used by the ordinary writer. The installer executor subsequently
// narrows this whole-device session with gpt.NewPartitionWriterAt; no mounted
// filesystem or command-line formatting tool participates in the build.
func (b *backend) OpenInstallerTarget(ctx context.Context, d device.Device) (device.InstallerTarget, error) {
	return b.open(ctx, d, os.O_RDWR)
}

// OpenInstallerReader re-runs Disk Arbitration/IOKit identity validation and
// opens an independent descriptor for semantic read-back verification.
func (b *backend) OpenInstallerReader(ctx context.Context, d device.Device) (device.InstallerReader, error) {
	return b.open(ctx, d, os.O_RDONLY)
}
func (b *backend) Flush(ctx context.Context, d device.Device) error {
	f, err := b.open(ctx, d, os.O_WRONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (b *backend) Eject(ctx context.Context, selected device.Device) error {
	d, err := b.diskSnapshot(ctx, selected)
	if err != nil {
		return err
	}
	return b.manager.Eject(ctx, d)
}
func (b *backend) FormatFAT32(ctx context.Context, selected device.Device, label string, updates chan<- progress.Update) error {
	ctx, cancel := context.WithTimeout(ctx, formatTimeout)
	defer cancel()

	label, err := formatLabel(label)
	if err != nil {
		return err
	}
	d, target, err := b.prepareFormat(ctx, selected)
	if err != nil {
		return err
	}
	defer target.Close()

	if err = fat32.Format(ctx, target, d.Size, label, formatProgress(ctx, updates)); err != nil {
		return fmt.Errorf("format FAT32: %w", err)
	}
	return nil
}

func formatProgress(ctx context.Context, updates chan<- progress.Update) func(uint64) {
	return func(percent uint64) {
		if updates == nil {
			return
		}
		select {
		case updates <- progress.Update{Stage: progress.StageFormatting, BytesProcessed: percent, TotalBytes: 100}:
		case <-ctx.Done():
		default:
		}
	}
}

func formatLabel(label string) (string, error) {
	if label == "" {
		label = "GOFLASHER"
	}
	if !fat32.ValidLabel(label) {
		return "", errors.New("format FAT32: invalid volume label")
	}
	return label, nil
}

func (b *backend) prepareFormat(ctx context.Context, selected device.Device) (disk.Disk, formatTarget, error) {
	d, err := b.diskSnapshot(ctx, selected)
	if err != nil {
		return disk.Disk{}, nil, err
	}
	if err := b.manager.Unmount(ctx, d); err != nil {
		return disk.Disk{}, nil, err
	}
	d, err = b.diskSnapshot(ctx, selected)
	if err != nil {
		return disk.Disk{}, nil, err
	}
	if d.Mounted {
		return disk.Disk{}, nil, ErrUnmountFailed
	}
	opener := b.openFormat
	if opener == nil {
		opener = func(path string) (formatTarget, error) {
			return openLocked(path, os.O_RDWR|syscall.O_CLOEXEC|exclusiveLockFlags)
		}
	}
	target, err := opener(d.Device)
	if err != nil {
		return disk.Disk{}, nil, fmt.Errorf("format FAT32: %w", err)
	}
	if err := b.validateOpenedDisk(ctx, d, target.Fd()); err != nil {
		_ = target.Close()
		return disk.Disk{}, nil, fmt.Errorf("format FAT32: %w", err)
	}
	return d, target, nil
}
func wholeDiskNumber(identifier string) (uint32, bool) {
	digits := strings.TrimPrefix(identifier, "disk")
	if digits == identifier || digits == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(digits, 10, 32)
	return uint32(n), err == nil
}
