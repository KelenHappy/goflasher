// Package fat32 contains the platform-independent, in-process FAT32 formatter
// used by every native disk backend. It never invokes an operating-system
// formatter or other external program.
package fat32

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
)

type Device interface {
	io.WriterAt
	Sync() error
}
type ProgressFunc func(uint64)

const sectorSize = uint64(512)
const reserved = uint64(32)
const fatCount = uint64(2)

type layout struct{ totalSectors, sectorsPerCluster, fatSectors, clusters uint64 }

func Format(ctx context.Context, device Device, size uint64, label string, progress ProgressFunc) error {
	return FormatPartition(ctx, device, size, label, progress)
}

// FormatPartition creates a FAT32 filesystem on a partition-relative device
// view. partitionSize is the length of that view, not the size of the backing
// disk. Callers formatting a partitioned disk must supply a bounded view so
// every offset used below is relative to the beginning of the partition.
//
// Format remains the whole-device (superfloppy) entry point for callers which
// intentionally erase and format an entire device.
func FormatPartition(ctx context.Context, partition Device, partitionSize uint64, label string, progress ProgressFunc) error {
	if !ValidLabel(label) {
		return errors.New("invalid FAT32 volume label")
	}
	l, err := newLayout(partitionSize)
	if err != nil {
		return err
	}
	f := formatter{ctx: ctx, device: partition, layout: l, label: label, progress: progress}
	return f.run()
}
func ValidLabel(label string) bool {
	if label == "" || len(label) > 11 {
		return false
	}
	for _, r := range label {
		if !contains("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-", r) {
			return false
		}
	}
	return true
}
func contains(s string, r rune) bool {
	for _, v := range s {
		if v == r {
			return true
		}
	}
	return false
}
func newLayout(size uint64) (layout, error) {
	if size < 64<<20 || size/sectorSize > uint64(^uint32(0)) {
		return layout{}, errors.New("device size is not supported by FAT32")
	}
	total := size / sectorSize
	spc := sectorsPerCluster(size)
	fs := requiredFATSectors(total, spc)
	data := total - reserved - fatCount*fs
	clusters := data / spc
	if clusters < 65525 {
		return layout{}, errors.New("device is too small for FAT32")
	}
	return layout{total, spc, fs, clusters}, nil
}
func requiredFATSectors(total, spc uint64) uint64 {
	low := uint64(1)
	high := (((total-reserved)/spc+2)*4 + sectorSize - 1) / sectorSize
	for low < high {
		middle := low + (high-low)/2
		clusters := (total - reserved - fatCount*middle) / spc
		required := ((clusters+2)*4 + sectorSize - 1) / sectorSize
		if required <= middle {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return low
}

type formatter struct {
	ctx      context.Context
	device   Device
	layout   layout
	label    string
	progress ProgressFunc
}

func (f *formatter) run() error {
	phases := []struct {
		percent uint64
		fn      func() error
	}{{10, f.clearPrimary}, {15, f.clearBackup}, {25, f.writeHeaders}, {80, f.writeFATs}, {90, f.writeRoot}, {100, f.device.Sync}}
	for _, p := range phases {
		if err := f.ctx.Err(); err != nil {
			return err
		}
		if err := p.fn(); err != nil {
			return err
		}
		if f.progress != nil {
			f.progress(p.percent)
		}
	}
	return nil
}
func (f *formatter) clearPrimary() error {
	return writeFullAt(f.ctx, f.device, make([]byte, reserved*sectorSize), 0)
}
func (f *formatter) clearBackup() error {
	return writeFullAt(f.ctx, f.device, make([]byte, 33*sectorSize), int64((f.layout.totalSectors-33)*sectorSize))
}
func (f *formatter) writeHeaders() error {
	boot := bootSector(uint32(f.layout.totalSectors), uint32(f.layout.fatSectors), byte(f.layout.sectorsPerCluster), f.label)
	info := fsInfo(uint32(f.layout.clusters))
	for _, w := range []struct {
		off int64
		b   []byte
	}{{0, boot}, {512, info}, {6 * 512, boot}, {7 * 512, info}} {
		if err := writeFullAt(f.ctx, f.device, w.b, w.off); err != nil {
			return err
		}
	}
	return nil
}
func (f *formatter) writeFATs() error {
	fat := make([]byte, f.layout.fatSectors*sectorSize)
	binary.LittleEndian.PutUint32(fat[0:4], 0x0ffffff8)
	binary.LittleEndian.PutUint32(fat[4:8], 0x0fffffff)
	binary.LittleEndian.PutUint32(fat[8:12], 0x0fffffff)
	for i := uint64(0); i < fatCount; i++ {
		if err := writeFullAt(f.ctx, f.device, fat, int64((reserved+i*f.layout.fatSectors)*sectorSize)); err != nil {
			return err
		}
	}
	return nil
}
func (f *formatter) writeRoot() error {
	root := make([]byte, f.layout.sectorsPerCluster*sectorSize)
	copy(root[:11], fatLabel(f.label))
	root[11] = 0x08
	return writeFullAt(f.ctx, f.device, root, int64((reserved+fatCount*f.layout.fatSectors)*sectorSize))
}
func writeFullAt(ctx context.Context, w io.WriterAt, p []byte, off int64) error {
	for len(p) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := w.WriteAt(p, off)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
		off += int64(n)
	}
	return nil
}
func sectorsPerCluster(size uint64) uint64 {
	switch {
	case size <= 260<<20:
		return 1
	case size <= 8<<30:
		return 8
	case size <= 16<<30:
		return 16
	case size <= 32<<30:
		return 32
	default:
		return 64
	}
}

// fatLabel writes into a fixed 11-byte, space-padded field. copy() bounds the
// write to min(11, len(label)), so even if ValidLabel is ever loosened, an
// over-long or arbitrary-byte label cannot overflow this field — the worst case
// is a spec-nonconformant label, not a memory-safety or injection issue.
func fatLabel(label string) []byte { v := []byte("           "); copy(v, label); return v }
func bootSector(total, fatSectors uint32, spc byte, label string) []byte {
	b := make([]byte, 512)
	copy(b[0:3], []byte{0xeb, 0x58, 0x90})
	copy(b[3:11], "GOFLASH ")
	binary.LittleEndian.PutUint16(b[11:13], 512)
	b[13] = spc
	binary.LittleEndian.PutUint16(b[14:16], 32)
	b[16] = 2
	b[21] = 0xf8
	binary.LittleEndian.PutUint16(b[24:26], 63)
	binary.LittleEndian.PutUint16(b[26:28], 255)
	binary.LittleEndian.PutUint32(b[32:36], total)
	binary.LittleEndian.PutUint32(b[36:40], fatSectors)
	binary.LittleEndian.PutUint32(b[44:48], 2)
	binary.LittleEndian.PutUint16(b[48:50], 1)
	binary.LittleEndian.PutUint16(b[50:52], 6)
	b[64], b[66] = 0x80, 0x29
	binary.LittleEndian.PutUint32(b[67:71], 0x47464c53)
	copy(b[71:82], fatLabel(label))
	copy(b[82:90], "FAT32   ")
	b[510], b[511] = 0x55, 0xaa
	return b
}
func fsInfo(clusters uint32) []byte {
	b := make([]byte, 512)
	binary.LittleEndian.PutUint32(b[0:4], 0x41615252)
	binary.LittleEndian.PutUint32(b[484:488], 0x61417272)
	binary.LittleEndian.PutUint32(b[488:492], clusters-1)
	binary.LittleEndian.PutUint32(b[492:496], 3)
	binary.LittleEndian.PutUint32(b[508:512], 0xaa550000)
	return b
}
