// Package blockio adapts byte-granular random access to raw devices that only
// accept whole logical sectors, such as Windows \\.\PhysicalDriveN handles.
// Partial sectors are completed with read-modify-write; bytes outside the
// caller's range are always preserved.
package blockio

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
)

// maxTransfer bounds each request forwarded to the device and the bounce
// buffer used for unaligned reads. It is a multiple of every valid sector size.
const maxTransfer = 1 << 20

const (
	minSectorSize = 512
	maxSectorSize = 64 << 10
)

var (
	ErrInvalidGeometry = errors.New("invalid block device geometry")
	ErrOutOfRange      = errors.New("block device access out of range")
	errInvalidCount    = errors.New("block device returned an invalid byte count")
)

// Device is the sector-granular random-access target being adapted.
type Device interface {
	io.ReaderAt
	io.WriterAt
}

// Aligned turns arbitrary ReadAt and WriteAt calls into sector-aligned device
// requests within a fixed device size. It is safe for concurrent use;
// read-modify-write cycles never interleave.
type Aligned struct {
	mu     sync.Mutex
	dev    Device
	sector int64
	size   int64
}

// NewAligned binds dev to its logical sector size and total byte size. The
// size must be a whole number of sectors so that every read-modify-write stays
// inside the device.
func NewAligned(dev Device, sectorSize uint32, size uint64) (*Aligned, error) {
	if dev == nil {
		return nil, fmt.Errorf("%w: missing device", ErrInvalidGeometry)
	}
	if !validSectorSize(sectorSize) {
		return nil, fmt.Errorf("%w: logical sector size %d", ErrInvalidGeometry, sectorSize)
	}
	if size == 0 || size > math.MaxInt64 || size%uint64(sectorSize) != 0 {
		return nil, fmt.Errorf("%w: device size %d is not a positive multiple of %d", ErrInvalidGeometry, size, sectorSize)
	}
	return &Aligned{dev: dev, sector: int64(sectorSize), size: int64(size)}, nil
}

func validSectorSize(size uint32) bool {
	return size >= minSectorSize && size <= maxSectorSize && size&(size-1) == 0
}

// ReadAt follows io.ReaderAt: a read that reaches the end of the device
// returns the available bytes together with io.EOF.
func (a *Aligned) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, ErrOutOfRange
	}
	if off >= a.size {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), a.size-off))
	a.mu.Lock()
	err := a.readRange(p[:n], off)
	a.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// readRange copies [off, off+len(p)) into p, reading whole sectors through a
// bounded bounce buffer whenever the range is not aligned.
func (a *Aligned) readRange(p []byte, off int64) error {
	if a.aligned(off) && a.aligned(int64(len(p))) {
		return a.transfer(p, off, a.readSectors)
	}
	end := off + int64(len(p))
	bounce := make([]byte, min(a.ceil(end)-a.floor(off), maxTransfer))
	for pos := off; pos < end; {
		start := a.floor(pos)
		stop := min(a.ceil(end), start+maxTransfer)
		chunk := bounce[:stop-start]
		if err := a.readSectors(chunk, start); err != nil {
			return err
		}
		copied := copy(p[pos-off:], chunk[pos-start:])
		pos += int64(copied)
	}
	return nil
}

// WriteAt follows io.WriterAt. A range that does not fit inside the device is
// rejected before any byte is written.
func (a *Aligned) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off > a.size || int64(len(p)) > a.size-off {
		return 0, ErrOutOfRange
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	written := 0
	for written < len(p) {
		n, err := a.writeStep(p[written:], off+int64(written))
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// writeStep writes the longest prefix of p that is either one partial sector
// or a run of whole aligned sectors, and reports how many bytes it consumed.
func (a *Aligned) writeStep(p []byte, off int64) (int, error) {
	if !a.aligned(off) || int64(len(p)) < a.sector {
		return a.patchSector(p, off)
	}
	whole := int(min(int64(len(p))/a.sector*a.sector, maxTransfer))
	if err := a.writeSectors(p[:whole], off); err != nil {
		return 0, err
	}
	return whole, nil
}

// patchSector merges the part of p that falls into off's sector with the
// sector's current contents and writes the sector back.
func (a *Aligned) patchSector(p []byte, off int64) (int, error) {
	start := a.floor(off)
	sector := make([]byte, a.sector)
	if err := a.readSectors(sector, start); err != nil {
		return 0, err
	}
	n := copy(sector[off-start:], p)
	if err := a.writeSectors(sector, start); err != nil {
		return 0, err
	}
	return n, nil
}

// transfer splits an aligned range into bounded device requests.
func (a *Aligned) transfer(p []byte, off int64, op func([]byte, int64) error) error {
	for len(p) > 0 {
		n := min(len(p), maxTransfer)
		if err := op(p[:n], off); err != nil {
			return err
		}
		p = p[n:]
		off += int64(n)
	}
	return nil
}

// readSectors fills p from an aligned offset. The range is inside the device,
// so a short read is an error even when the device reports io.EOF.
func (a *Aligned) readSectors(p []byte, off int64) error {
	for len(p) > 0 {
		n, err := a.dev.ReadAt(p, off)
		if n < 0 || n > len(p) {
			return errInvalidCount
		}
		p = p[n:]
		off += int64(n)
		switch {
		case len(p) == 0:
			return nil
		case errors.Is(err, io.EOF):
			return io.ErrUnexpectedEOF
		case err != nil:
			return err
		case n == 0:
			return io.ErrNoProgress
		}
	}
	return nil
}

func (a *Aligned) writeSectors(p []byte, off int64) error {
	return a.transfer(p, off, func(chunk []byte, at int64) error {
		n, err := a.dev.WriteAt(chunk, at)
		if err != nil {
			return err
		}
		if n != len(chunk) {
			return io.ErrShortWrite
		}
		return nil
	})
}

func (a *Aligned) aligned(v int64) bool { return v%a.sector == 0 }
func (a *Aligned) floor(v int64) int64  { return v / a.sector * a.sector }
func (a *Aligned) ceil(v int64) int64   { return (v + a.sector - 1) / a.sector * a.sector }
