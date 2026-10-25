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

	"github.com/goflasher/goflasher/internal/blockio"
	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/progress"
)

var (
	ErrUnsupportedDevice         = errors.New("unsupported device")
	ErrSystemDisk                = errors.New("system disk")
	ErrDeviceChanged             = errors.New("device identity changed")
	ErrUnmountFailed             = errors.New("could not lock and dismount every volume; run GoFlasher as administrator")
	ErrSystemTopologyUnavailable = errors.New("Windows system topology unavailable")
	ErrVolumeTopologyUnavailable = errors.New("Windows volume topology unavailable")
	ErrVolumeLockDenied          = errors.New("Windows volume lock denied")
	ErrUnsupportedSectorSize     = errors.New("unsupported logical sector size")
)

type windowsIdentityEvidence struct {
	Serial string
	WWN    string
}

// diskRecord is deliberately free of Win32 handles. A snapshot can safely be
// passed to policy code and tests; handles remain owned by nativeAPI.
type diskRecord struct {
	device.Device
	identity                                 windowsIdentityEvidence
	deviceHotplug, mediaHotplug, usbAncestor bool
	deviceNumber                             diskNumber
	devInst                                  devNode
}

type nativeAPI interface {
	list(context.Context) ([]diskRecord, int, error)
	lockVolumes(context.Context, diskNumber) (volumeLocks, error)
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
var _ device.WindowsInstallerBackend = (*Backend)(nil)

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
	if !hasPersistentIdentity(*d) {
		d.RejectReason = "no trustworthy persistent identity"
		return
	}
	if !isSupportedDisk(*r) {
		return
	}
	d.IsAllowed = true
	d.RejectReason = ""
}

func isSupportedDisk(r diskRecord) bool {
	if !hasUSBConnection(r) || !isHotplugDisk(r) {
		return false
	}
	if !hasUsableLocation(r.Device) {
		return false
	}
	return isPhysicalTransport(r.Transport)
}

// Bus type is corroborating evidence, never the sole admission criterion.
func hasUSBConnection(r diskRecord) bool {
	return r.usbAncestor || strings.EqualFold(r.Transport, "usb")
}

func isHotplugDisk(r diskRecord) bool {
	return r.deviceHotplug || r.mediaHotplug
}

func hasPersistentIdentity(d device.Device) bool {
	return d.Serial != "" || d.WWN != ""
}

func hasUsableLocation(d device.Device) bool {
	return d.Size != 0 && d.Path != ""
}

func isPhysicalTransport(transport string) bool {
	return !strings.EqualFold(transport, "virtual") &&
		!strings.EqualFold(transport, "filebackedvirtual")
}

func (b *Backend) records(ctx context.Context) ([]diskRecord, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	rs, skipped, err := b.native().list(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("enumerate Windows disks: %w", err)
	}
	for i := range rs {
		classify(&rs[i])
	}
	return rs, skipped, nil
}
func (b *Backend) ListAllowedDevices(ctx context.Context) ([]device.Device, device.ScanReport, error) {
	rs, skipped, err := b.records(ctx)
	if err != nil {
		return nil, device.ScanReport{}, err
	}
	var out []device.Device
	for _, r := range rs {
		if r.IsAllowed {
			out = append(out, r.Device)
		}
	}
	return out, device.ScanReport{Skipped: skipped}, nil
}
func (b *Backend) record(ctx context.Context, id string) (diskRecord, error) {
	rs, _, err := b.records(ctx)
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
	if deviceChanged(selected, r.Device) {
		return diskRecord{}, ErrDeviceChanged
	}
	return r, nil
}

func deviceChanged(selected, current device.Device) bool {
	if !device.SameIdentity(selected, current) {
		return true
	}
	return selected.Size != current.Size || selected.Model != current.Model
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
		return fmt.Errorf("%w: %w", ErrUnmountFailed, err)
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

// rawWriter streams an image through whole-sector requests. The image size
// need not be a sector multiple: Close merges the final partial sector before
// the handle flushes and closes.
type rawWriter struct {
	*blockio.Writer
	file *managedFile
}

func (w *rawWriter) Close() error { return errors.Join(w.Writer.Flush(), w.file.Close()) }

// rawReader reads back any byte length through whole-sector requests.
type rawReader struct {
	*io.SectionReader
	file *managedFile
}

func (r *rawReader) Close() error { return r.file.Close() }

func (b *Backend) OpenWriter(ctx context.Context, d device.Device) (io.WriteCloser, error) {
	f, err := b.open(ctx, d, true)
	if err != nil {
		return nil, err
	}
	disk, _, err := alignedDisk(f, d.Size)
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return &rawWriter{Writer: blockio.NewWriter(disk), file: f}, nil
}
func (b *Backend) OpenReader(ctx context.Context, d device.Device) (io.ReadCloser, error) {
	f, err := b.open(ctx, d, false)
	if err != nil {
		return nil, err
	}
	disk, _, err := alignedDisk(f, d.Size)
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return &rawReader{SectionReader: io.NewSectionReader(disk, 0, int64(d.Size)), file: f}, nil
}

// installerSectorSize is the only logical sector size the installer's GPT
// layout is computed for; any other geometry fails closed.
const installerSectorSize = 512

// installerSession is a raw disk handle narrowed for the installer builder.
// \\.\PhysicalDriveN accepts only whole-sector requests, while the FAT32
// builder and verifier address individual bytes, so all I/O passes through a
// read-modify-write adapter bounded by the validated disk size.
type installerSession struct {
	*blockio.Aligned
	file *managedFile
}

func (s *installerSession) Sync() error  { return s.file.Flush() }
func (s *installerSession) Close() error { return s.file.Close() }

// sectorSizer is implemented by native files that can report the logical
// sector size of their own already-validated handle.
type sectorSizer interface {
	logicalSectorSize() (uint32, error)
}

// OpenInstallerTarget opens the same identity-checked write handle as
// OpenWriter, so Unmount must already hold every volume lock on the disk.
func (b *Backend) OpenInstallerTarget(ctx context.Context, d device.Device) (device.InstallerTarget, error) {
	return b.openInstallerSession(ctx, d, true)
}

// OpenInstallerReader re-runs identity validation and opens an independent
// read handle for semantic read-back verification.
func (b *Backend) OpenInstallerReader(ctx context.Context, d device.Device) (device.InstallerReader, error) {
	return b.openInstallerSession(ctx, d, false)
}

func (b *Backend) openInstallerSession(ctx context.Context, d device.Device, write bool) (*installerSession, error) {
	f, err := b.open(ctx, d, write)
	if err != nil {
		return nil, err
	}
	session, err := newInstallerSession(f, d.Size)
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return session, nil
}

func newInstallerSession(f *managedFile, size uint64) (*installerSession, error) {
	disk, sector, err := alignedDisk(f, size)
	if err != nil {
		return nil, err
	}
	if sector != installerSectorSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrUnsupportedSectorSize, sector)
	}
	return &installerSession{Aligned: disk, file: f}, nil
}

// alignedDisk binds an opened raw handle to its own reported logical sector
// size and the validated disk size, so every request it forwards is a whole
// number of sectors inside the disk.
func alignedDisk(f *managedFile, size uint64) (*blockio.Aligned, uint32, error) {
	random, ok := f.nativeFile.(blockio.Device)
	if !ok {
		return nil, 0, errors.New("raw disk does not support random access")
	}
	sizer, ok := f.nativeFile.(sectorSizer)
	if !ok {
		return nil, 0, fmt.Errorf("%w: geometry is unavailable", ErrUnsupportedSectorSize)
	}
	sector, err := sizer.logicalSectorSize()
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrUnsupportedSectorSize, err)
	}
	disk, err := blockio.NewAligned(random, sector, size)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrUnsupportedSectorSize, err)
	}
	return disk, sector, nil
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
