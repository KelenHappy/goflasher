//go:build windows

// Package windows implements Windows disk access directly through the Win32
// storage APIs. It intentionally never invokes a command interpreter.
package windows

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/progress"
)

var (
	ErrUnsupportedDevice = errors.New("unsupported device")
	ErrSystemDisk        = errors.New("system disk")
	ErrDeviceChanged     = errors.New("device identity changed")
	ErrUnmountFailed     = errors.New("could not lock and dismount every volume; run GoFlasher as administrator")
)

// diskRecord is deliberately free of Win32 handles. A snapshot can safely be
// passed to policy code and tests; handles remain owned by nativeAPI.
type diskRecord struct {
	device.Device
	deviceHotplug, mediaHotplug, usbAncestor bool
	deviceNumber                             uint32
	devInst                                  uint32
}

type nativeAPI interface {
	list(context.Context) ([]diskRecord, error)
	lockVolumes(context.Context, uint32) (volumeLocks, error)
	openDisk(context.Context, diskRecord, bool) (nativeFile, error)
	eject(context.Context, diskRecord) error
	formatFAT32(context.Context, diskRecord, string, chan<- progress.Update) error
}
type nativeFile interface {
	io.ReadWriteCloser
	Flush() error
}
type volumeLocks interface{ Close() error }

type Backend struct {
	api   nativeAPI
	mu    sync.Mutex
	locks map[string]volumeLocks
}

var _ device.Backend = (*Backend)(nil)
var _ device.FAT32Formatter = (*Backend)(nil)

func NewBackend() *Backend { return &Backend{api: newWinAPI(), locks: make(map[string]volumeLocks)} }
func (b *Backend) native() nativeAPI {
	if b.api == nil {
		b.api = newWinAPI()
	}
	return b.api
}

func classify(r *diskRecord) {
	d := &r.Device
	d.IsAllowed = false
	d.RejectReason = ErrUnsupportedDevice.Error()
	if d.IsSystemDisk {
		d.RejectReason = ErrSystemDisk.Error()
		return
	}
	// Bus type is corroborating evidence, never the sole admission criterion.
	external := r.usbAncestor || strings.EqualFold(d.Transport, "usb")
	hotplug := r.deviceHotplug || r.mediaHotplug
	identity := d.Serial != "" || d.WWN != ""
	if external && hotplug && identity && d.Size != 0 && d.Path != "" &&
		!strings.EqualFold(d.Transport, "virtual") && !strings.EqualFold(d.Transport, "filebackedvirtual") {
		d.IsAllowed = true
		d.RejectReason = ""
	}
}

func (b *Backend) records(ctx context.Context) ([]diskRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rs, err := b.native().list(ctx)
	if err != nil {
		return nil, fmt.Errorf("enumerate Windows disks: %w", err)
	}
	for i := range rs {
		classify(&rs[i])
	}
	return rs, nil
}
func (b *Backend) ListAllowedDevices(ctx context.Context) ([]device.Device, error) {
	rs, err := b.records(ctx)
	if err != nil {
		return nil, err
	}
	var out []device.Device
	for _, r := range rs {
		if r.IsAllowed {
			out = append(out, r.Device)
		}
	}
	return out, nil
}
func (b *Backend) record(ctx context.Context, id string) (diskRecord, error) {
	rs, err := b.records(ctx)
	if err != nil {
		return diskRecord{}, err
	}
	for _, r := range rs {
		if r.ID == id {
			return r, nil
		}
	}
	return diskRecord{}, os.ErrNotExist
}
func (b *Backend) RefreshDevice(ctx context.Context, id string) (device.Device, error) {
	r, err := b.record(ctx, id)
	return r.Device, err
}
func (b *Backend) revalidate(ctx context.Context, selected device.Device) (diskRecord, error) {
	r, err := b.record(ctx, selected.ID)
	if err != nil {
		return diskRecord{}, fmt.Errorf("%w: %v", ErrDeviceChanged, err)
	}
	if !r.IsAllowed {
		return diskRecord{}, fmt.Errorf("%w: %s", ErrUnsupportedDevice, r.RejectReason)
	}
	if !device.SameIdentity(selected, r.Device) || selected.Size != r.Size || selected.Model != r.Model {
		return diskRecord{}, ErrDeviceChanged
	}
	return r, nil
}
func (b *Backend) release(id string) error {
	b.mu.Lock()
	l := b.locks[id]
	delete(b.locks, id)
	b.mu.Unlock()
	if l != nil {
		return l.Close()
	}
	return nil
}

// ReleaseDevice releases volume locks after the service has completed flush,
// optional verification, and eject processing.
func (b *Backend) ReleaseDevice(d device.Device) error { return b.release(d.ID) }
func (b *Backend) Unmount(ctx context.Context, d device.Device) error {
	r, err := b.revalidate(ctx, d)
	if err != nil {
		return err
	}
	_ = b.release(d.ID)
	l, err := b.native().lockVolumes(ctx, r.deviceNumber)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnmountFailed, err)
	}
	b.mu.Lock()
	b.locks[d.ID] = l
	b.mu.Unlock()
	return nil
}

type managedFile struct {
	nativeFile
	once         sync.Once
	flushOnClose bool
	closeErr     error
}

func (f *managedFile) Close() error {
	f.once.Do(func() {
		if f.flushOnClose {
			if err := f.nativeFile.Flush(); err != nil {
				f.closeErr = err
			}
		}
		if err := f.nativeFile.Close(); f.closeErr == nil {
			f.closeErr = err
		}
	})
	return f.closeErr
}
func (b *Backend) open(ctx context.Context, d device.Device, write bool) (*managedFile, error) {
	r, err := b.revalidate(ctx, d)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	l := b.locks[d.ID]
	b.mu.Unlock()
	if write && l == nil {
		return nil, ErrUnmountFailed
	}
	// native open performs identity IOCTLs on the returned handle, closing the
	// disk-number reuse window between validation and destructive access.
	f, err := b.native().openDisk(ctx, r, write)
	if err != nil {
		return nil, err
	}
	return &managedFile{nativeFile: f, flushOnClose: write}, nil
}
func (b *Backend) OpenWriter(ctx context.Context, d device.Device) (io.WriteCloser, error) {
	return b.open(ctx, d, true)
}
func (b *Backend) OpenReader(ctx context.Context, d device.Device) (io.ReadCloser, error) {
	return b.open(ctx, d, false)
}
func (b *Backend) Flush(ctx context.Context, d device.Device) error {
	// FlushFileBuffers requires a handle opened with write access.
	f, err := b.open(ctx, d, true)
	if err != nil {
		return err
	}
	// Flush explicitly below; Close only owns the handle for this short-lived
	// flush operation and must not issue a duplicate FlushFileBuffers call.
	f.flushOnClose = false
	defer f.Close()
	return f.Flush()
}
func (b *Backend) Eject(ctx context.Context, d device.Device) error {
	r, err := b.revalidate(ctx, d)
	if err != nil {
		return err
	}
	defer b.release(d.ID)
	return b.native().eject(ctx, r)
}
func (b *Backend) FormatFAT32(ctx context.Context, d device.Device, label string, updates chan<- progress.Update) error {
	r, err := b.revalidate(ctx, d)
	if err != nil {
		return err
	}
	if err := b.Unmount(ctx, r.Device); err != nil {
		return err
	}
	r, err = b.revalidate(ctx, r.Device)
	if err != nil {
		_ = b.release(d.ID)
		return err
	}
	b.mu.Lock()
	l := b.locks[d.ID]
	b.mu.Unlock()
	if l == nil {
		return ErrUnmountFailed
	}
	defer b.release(d.ID)
	return b.native().formatFAT32(ctx, r, label, updates)
}
