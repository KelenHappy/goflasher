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
type Backend struct {
	manager    disk.Manager
	openRaw    func(string, int) (*os.File, error)
	openFormat func(string) (formatTarget, error)
	fstat      func(int, *syscall.Stat_t) error
	stat       func(string, *syscall.Stat_t) error
}

var _ device.Backend = (*Backend)(nil)

func NewBackend() *Backend { return NewBackendWithManager(disk.NewManager()) }
func NewBackendWithManager(manager disk.Manager) *Backend {
	return &Backend{manager: manager, openRaw: openRawFile, openFormat: openFormatTarget}
}
func openRawFile(path string, flag int) (*os.File, error) { return os.OpenFile(path, flag, 0) }
func openFormatTarget(path string) (formatTarget, error) {
	return os.OpenFile(path, os.O_RDWR|syscall.O_CLOEXEC, 0)
}

func deviceFromDisk(d disk.Disk) device.Device {
	n, _ := wholeDiskNumber(strings.TrimPrefix(d.Device, "/dev/r"))
	return device.Device{ID: d.ID, Path: d.Device, Vendor: d.Vendor, Model: d.Model, Serial: d.Serial, Transport: d.Bus, SysfsPath: d.RegistryPath, Major: n, Size: d.Size, IsCardReader: d.Ejectable, Mounted: d.Mounted, IsSystemDisk: d.System, IsAllowed: d.Removable && d.External && !d.System && d.Bus == "usb" && d.Size > 0 && d.RegistryID != "", MountPoints: append([]string(nil), d.MountPoints...), PartitionCount: len(d.MountPoints)}
}
func (b *Backend) ListAllowedDevices(ctx context.Context) ([]device.Device, error) {
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
func (b *Backend) RefreshDevice(ctx context.Context, id string) (device.Device, error) {
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
func (b *Backend) diskSnapshot(ctx context.Context, selected device.Device) (disk.Disk, error) {
	d, err := b.manager.Refresh(ctx, selected.ID)
	if err != nil {
		return disk.Disk{}, fmt.Errorf("%w: %w", ErrDeviceChanged, err)
	}
	fresh := deviceFromDisk(d)
	if !fresh.IsAllowed {
		return disk.Disk{}, ErrUnsupportedDevice
	}
	if !device.SameIdentity(selected, fresh) || selected.Size != fresh.Size || selected.Model != fresh.Model {
		return disk.Disk{}, ErrDeviceChanged
	}
	return d, nil
}
func (b *Backend) Unmount(ctx context.Context, selected device.Device) error {
	d, err := b.diskSnapshot(ctx, selected)
	if err != nil {
		return err
	}
	return b.manager.Unmount(ctx, d)
}
func (b *Backend) open(ctx context.Context, selected device.Device, flag int) (*os.File, error) {
	d, err := b.diskSnapshot(ctx, selected)
	if err != nil {
		return nil, err
	}
	if d.Mounted {
		return nil, ErrUnmountFailed
	}
	opener := b.openRaw
	if opener == nil {
		opener = openRawFile
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
func (b *Backend) validateOpenedDisk(ctx context.Context, authorized disk.Disk, fd uintptr) error {
	fstat := b.fstat
	if fstat == nil {
		fstat = syscall.Fstat
	}
	var opened syscall.Stat_t
	if err := fstat(int(fd), &opened); err != nil || opened.Mode&syscall.S_IFMT != syscall.S_IFCHR {
		return ErrDeviceChanged
	}
	fresh, err := b.manager.Refresh(ctx, authorized.ID)
	if err != nil || fresh.Mounted || !deviceFromDisk(fresh).IsAllowed || !disk.SameIdentity(authorized, fresh) {
		return ErrDeviceChanged
	}
	stat := b.stat
	if stat == nil {
		stat = syscall.Stat
	}
	var node syscall.Stat_t
	if err := stat(fresh.Device, &node); err != nil || node.Mode&syscall.S_IFMT != syscall.S_IFCHR || node.Rdev != opened.Rdev {
		return ErrDeviceChanged
	}
	return nil
}
func (b *Backend) OpenWriter(ctx context.Context, d device.Device) (io.WriteCloser, error) {
	return b.open(ctx, d, os.O_WRONLY)
}
func (b *Backend) OpenReader(ctx context.Context, d device.Device) (io.ReadCloser, error) {
	return b.open(ctx, d, os.O_RDONLY)
}
func (b *Backend) Flush(ctx context.Context, d device.Device) error {
	f, err := b.open(ctx, d, os.O_WRONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (b *Backend) Eject(ctx context.Context, selected device.Device) error {
	d, err := b.diskSnapshot(ctx, selected)
	if err != nil {
		return err
	}
	return b.manager.Eject(ctx, d)
}
func (b *Backend) FormatFAT32(ctx context.Context, selected device.Device, label string, updates chan<- progress.Update) error {
	ctx, cancel := context.WithTimeout(ctx, formatTimeout)
	defer cancel()
	d, err := b.diskSnapshot(ctx, selected)
	if err != nil {
		return err
	}
	if label == "" {
		label = "GOFLASHER"
	}
	if !fat32.ValidLabel(label) {
		return errors.New("format FAT32: invalid volume label")
	}
	if err = b.manager.Unmount(ctx, d); err != nil {
		return err
	}
	d, err = b.diskSnapshot(ctx, selected)
	if err != nil {
		return err
	}
	if d.Mounted {
		return ErrUnmountFailed
	}
	opener := b.openFormat
	if opener == nil {
		opener = openFormatTarget
	}
	target, err := opener(d.Device)
	if err != nil {
		return fmt.Errorf("format FAT32: %w", err)
	}
	defer target.Close()
	if err = b.validateOpenedDisk(ctx, d, target.Fd()); err != nil {
		return fmt.Errorf("format FAT32: %w", err)
	}
	if err = fat32.Format(ctx, target, d.Size, label, func(percent uint64) {
		if updates != nil {
			select {
			case updates <- progress.Update{Stage: progress.StageFormatting, BytesProcessed: percent, TotalBytes: 100}:
			case <-ctx.Done():
			default:
			}
		}
	}); err != nil {
		return fmt.Errorf("format FAT32: %w", err)
	}
	return nil
}
func wholeDiskNumber(identifier string) (uint32, bool) {
	digits := strings.TrimPrefix(identifier, "disk")
	if digits == identifier || digits == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(digits, 10, 32)
	return uint32(n), err == nil
}
