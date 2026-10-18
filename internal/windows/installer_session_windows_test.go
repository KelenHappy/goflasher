//go:build windows

package windows

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/goflasher/goflasher/internal/device"
)

// fakeDisk behaves like a \\.\PhysicalDriveN handle: requests that are not
// whole sectors fail, as ReadFile and WriteFile do on a raw disk.
type fakeDisk struct {
	data              []byte
	sector            uint32
	sectorErr         error
	flushes, closes   int
	misalignedRequest bool
}

var errFakeMisaligned = errors.New("fake disk: misaligned request")

func newFakeDisk(size int, sector uint32) *fakeDisk {
	return &fakeDisk{data: make([]byte, size), sector: sector}
}

func (d *fakeDisk) aligned(p []byte, off int64) bool {
	return off%int64(d.sector) == 0 && len(p)%int(d.sector) == 0
}

func (d *fakeDisk) ReadAt(p []byte, off int64) (int, error) {
	if !d.aligned(p, off) {
		d.misalignedRequest = true
		return 0, errFakeMisaligned
	}
	return copy(p, d.data[off:]), nil
}

func (d *fakeDisk) WriteAt(p []byte, off int64) (int, error) {
	if !d.aligned(p, off) {
		d.misalignedRequest = true
		return 0, errFakeMisaligned
	}
	return copy(d.data[off:], p), nil
}

func (*fakeDisk) Read([]byte) (int, error)             { return 0, errors.New("sequential read unused") }
func (*fakeDisk) Write([]byte) (int, error)            { return 0, errors.New("sequential write unused") }
func (d *fakeDisk) Close() error                       { d.closes++; return nil }
func (d *fakeDisk) Flush() error                       { d.flushes++; return nil }
func (d *fakeDisk) logicalSectorSize() (uint32, error) { return d.sector, d.sectorErr }

const fakeDiskSize = 64 << 10

func installerBackend(t *testing.T, disk nativeFile) (*Backend, *fakeAPI, device.Device) {
	t.Helper()
	r := candidate()
	r.Size = fakeDiskSize
	api := &fakeAPI{records: []diskRecord{r}, disk: disk}
	b := &Backend{api: api, locks: map[string]volumeLocks{}}
	return b, api, listedDevice(t, b)
}

func TestInstallerTargetRequiresVolumeLocks(t *testing.T) {
	disk := newFakeDisk(fakeDiskSize, 512)
	b, api, d := installerBackend(t, disk)
	_, err := b.OpenInstallerTarget(context.Background(), d)
	requireErrorIs(t, err, ErrUnmountFailed)
	requireEqual(t, "opens", api.opens, 0)
}

func TestInstallerSessionAlignsByteGranularIO(t *testing.T) {
	disk := newFakeDisk(fakeDiskSize, 512)
	b, api, d := installerBackend(t, disk)
	ctx := context.Background()
	requireNoError(t, b.Unmount(ctx, d))

	target, err := b.OpenInstallerTarget(ctx, d)
	requireNoError(t, err)
	payload := []byte("unaligned installer payload")
	n, err := target.WriteAt(payload, 1000)
	requireNoError(t, err)
	requireEqual(t, "written", n, len(payload))
	requireNoError(t, target.Sync())
	requireNoError(t, target.Close())

	reader, err := b.OpenInstallerReader(ctx, d)
	requireNoError(t, err)
	got := make([]byte, len(payload))
	_, err = reader.ReadAt(got, 1000)
	requireNoError(t, err)
	requireNoError(t, reader.Close())

	if !bytes.Equal(got, payload) {
		t.Fatalf("read back %q, want %q", got, payload)
	}
	requireEqual(t, "misaligned device request", disk.misalignedRequest, false)
	requireEqual(t, "open modes", len(api.openWrites), 2)
	requireEqual(t, "target opened for write", api.openWrites[0], true)
	requireEqual(t, "reader opened for write", api.openWrites[1], false)
	// Sync flushes explicitly and closing the write handle flushes once more;
	// the read handle never flushes.
	requireEqual(t, "flushes", disk.flushes, 2)
	requireEqual(t, "closes", disk.closes, 2)
}

func TestInstallerSessionRejectsUnsupportedGeometry(t *testing.T) {
	tests := []struct {
		name string
		disk nativeFile
	}{
		{"4Kn sectors", newFakeDisk(fakeDiskSize, 4096)},
		{"geometry query fails", &fakeDisk{data: make([]byte, fakeDiskSize), sector: 512, sectorErr: errors.New("IOCTL failed")}},
		{"no geometry", &ungaugedDisk{disk: newFakeDisk(fakeDiskSize, 512)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _, d := installerBackend(t, tt.disk)
			_, err := b.OpenInstallerReader(context.Background(), d)
			requireErrorIs(t, err, ErrUnsupportedSectorSize)
			assertClosedOnce(t, tt.disk)
		})
	}
}

func TestInstallerSessionRejectsSequentialOnlyFile(t *testing.T) {
	b, _, d := installerBackend(t, &sequentialFile{})
	_, err := b.OpenInstallerReader(context.Background(), d)
	requireErrorContains(t, err, "random access")
}

// sequentialFile is a nativeFile without ReadAt or WriteAt.
type sequentialFile struct{ fakeFile }

// ungaugedDisk supports random access but cannot report its sector size.
type ungaugedDisk struct{ disk *fakeDisk }

func (u *ungaugedDisk) ReadAt(p []byte, off int64) (int, error)  { return u.disk.ReadAt(p, off) }
func (u *ungaugedDisk) WriteAt(p []byte, off int64) (int, error) { return u.disk.WriteAt(p, off) }
func (u *ungaugedDisk) Read(p []byte) (int, error)               { return u.disk.Read(p) }
func (u *ungaugedDisk) Write(p []byte) (int, error)              { return u.disk.Write(p) }
func (u *ungaugedDisk) Close() error                             { return u.disk.Close() }
func (u *ungaugedDisk) Flush() error                             { return u.disk.Flush() }

func assertClosedOnce(t *testing.T, f nativeFile) {
	t.Helper()
	switch f := f.(type) {
	case *fakeDisk:
		requireEqual(t, "closes", f.closes, 1)
	case *ungaugedDisk:
		requireEqual(t, "closes", f.disk.closes, 1)
	default:
		t.Fatalf("unexpected fake %T", f)
	}
}

// TestRawWriterHandlesImageSizeThatIsNotASectorMultiple streams an image whose
// length leaves a partial final sector, then reads exactly that length back.
func TestRawWriterHandlesImageSizeThatIsNotASectorMultiple(t *testing.T) {
	disk := newFakeDisk(fakeDiskSize, 512)
	for i := range disk.data {
		disk.data[i] = 0xEE
	}
	b, _, d := installerBackend(t, disk)
	ctx := context.Background()
	requireNoError(t, b.Unmount(ctx, d))

	image := bytes.Repeat([]byte("goflasher"), 1111) // 9999 bytes
	w, err := b.OpenWriter(ctx, d)
	requireNoError(t, err)
	for rest := image; len(rest) > 0; {
		n := min(len(rest), 700)
		_, err := w.Write(rest[:n])
		requireNoError(t, err)
		rest = rest[n:]
	}
	requireNoError(t, w.Close())

	r, err := b.OpenReader(ctx, d)
	requireNoError(t, err)
	got := make([]byte, len(image))
	_, err = io.ReadFull(r, got)
	requireNoError(t, err)
	requireNoError(t, r.Close())

	if !bytes.Equal(got, image) {
		t.Fatal("read-back differs from the written image")
	}
	requireEqual(t, "byte after image", disk.data[len(image)], byte(0xEE))
	requireEqual(t, "misaligned device request", disk.misalignedRequest, false)
}
