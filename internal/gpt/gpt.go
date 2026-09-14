// Package gpt builds platform-independent GUID partition tables using logical
// sector LBAs. Byte offsets are derived only at the final WriterAt boundary.
package gpt

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math"
	"unicode/utf16"
)

const (
	EntryCount      uint32 = 128
	EntrySize       uint32 = 128
	HeaderSize      uint32 = 92
	alignmentBytes  uint64 = 1 << 20
	entryArrayBytes        = uint64(EntryCount) * uint64(EntrySize)
	espName                = "EFI System Partition"
)

var ErrInvalidLayout = errors.New("invalid GPT layout")

type GUID [16]byte

var EFITypeGUID = GUID{0xc1, 0x2a, 0x73, 0x28, 0xf8, 0x1f, 0x11, 0xd2, 0xba, 0x4b, 0x00, 0xa0, 0xc9, 0x3e, 0xc9, 0x3b}

// MarshalBinary returns EFI/GPT mixed-endian on-disk GUID bytes.
func (g GUID) MarshalBinary() []byte {
	return []byte{g[3], g[2], g[1], g[0], g[5], g[4], g[7], g[6], g[8], g[9], g[10], g[11], g[12], g[13], g[14], g[15]}
}

type Layout struct {
	LogicalSectorSize           uint64
	TotalLBAs                   uint64
	FirstUsableLBA              uint64
	LastUsableLBA               uint64
	PartitionStartLBA           uint64
	PartitionEndLBA             uint64
	PrimaryEntriesLBA           uint64
	BackupEntriesLBA            uint64
	DiskGUID                    GUID
	PartitionGUID               GUID
	primaryHeader, backupHeader []byte
	entries                     []byte
}

// geometry is the LBA placement of the GPT metadata and the single partition.
type geometry struct {
	last, firstUsable, lastUsable, backupEntries, start uint64
}

// Build creates one EFI System Partition. random may be nil to use crypto/rand.
func Build(totalLBAs, logicalSectorSize uint64, random io.Reader) (*Layout, error) {
	g, err := planGeometry(totalLBAs, logicalSectorSize)
	if err != nil {
		return nil, err
	}
	if random == nil {
		random = rand.Reader
	}
	l := &Layout{LogicalSectorSize: logicalSectorSize, TotalLBAs: totalLBAs, FirstUsableLBA: g.firstUsable, LastUsableLBA: g.lastUsable, PartitionStartLBA: g.start, PartitionEndLBA: g.lastUsable, PrimaryEntriesLBA: 2, BackupEntriesLBA: g.backupEntries, entries: make([]byte, entryArrayBytes)}
	if err := l.assignGUIDs(random); err != nil {
		return nil, err
	}
	l.encodePartitionEntry()
	entriesCRC := crc32.ChecksumIEEE(l.entries)
	l.primaryHeader = l.header(1, g.last, l.PrimaryEntriesLBA, entriesCRC)
	l.backupHeader = l.header(g.last, 1, g.backupEntries, entriesCRC)
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return l, nil
}

// planGeometry places the primary and backup metadata around a usable region
// and aligns the partition start to 1 MiB.
func planGeometry(totalLBAs, sectorSize uint64) (geometry, error) {
	if !validSectorSize(sectorSize) {
		return geometry{}, ErrInvalidLayout
	}
	if _, ok := mulInt64(totalLBAs, sectorSize); !ok {
		return geometry{}, ErrInvalidLayout
	}
	entrySectors, ok := ceilDiv(entryArrayBytes, sectorSize)
	if !ok || !metadataFits(totalLBAs, entrySectors) {
		return geometry{}, ErrInvalidLayout
	}
	g := geometry{last: totalLBAs - 1, firstUsable: 2 + entrySectors}
	g.backupEntries = g.last - entrySectors
	g.lastUsable = g.backupEntries - 1
	start, ok := alignedStart(g.firstUsable, sectorSize)
	g.start = start
	if !ok || g.start > g.lastUsable {
		return geometry{}, ErrInvalidLayout
	}
	return g, nil
}
func validSectorSize(size uint64) bool {
	return size >= 512 && size&(size-1) == 0
}

// metadataFits reports whether the protective MBR, both headers and both entry
// arrays fit on the disk with at least one LBA left between the arrays.
func metadataFits(totalLBAs, entrySectors uint64) bool {
	if totalLBAs < 4 {
		return false
	}
	return entrySectors <= totalLBAs-3
}
func alignedStart(firstUsable, sectorSize uint64) (uint64, bool) {
	alignLBAs, ok := ceilDiv(alignmentBytes, sectorSize)
	if !ok {
		return 0, false
	}
	return alignUp(firstUsable, alignLBAs)
}
func (l *Layout) assignGUIDs(random io.Reader) error {
	if err := fillGUID(random, &l.DiskGUID); err != nil {
		return err
	}
	if err := fillGUID(random, &l.PartitionGUID); err != nil {
		return err
	}
	if l.DiskGUID == l.PartitionGUID {
		return ErrInvalidLayout
	}
	return nil
}
func (l *Layout) encodePartitionEntry() {
	e := l.entries[:EntrySize]
	copy(e[0:16], EFITypeGUID.MarshalBinary())
	copy(e[16:32], l.PartitionGUID.MarshalBinary())
	binary.LittleEndian.PutUint64(e[32:40], l.PartitionStartLBA)
	binary.LittleEndian.PutUint64(e[40:48], l.PartitionEndLBA)
	for i, v := range utf16.Encode([]rune(espName)) {
		binary.LittleEndian.PutUint16(e[56+i*2:], v)
	}
}
func fillGUID(r io.Reader, g *GUID) error {
	if _, err := io.ReadFull(r, g[:]); err != nil {
		return err
	}
	g[6] = (g[6] & 0x0f) | 0x40
	g[8] = (g[8] & 0x3f) | 0x80
	return nil
}
func (l *Layout) header(current, alternate, entriesLBA uint64, entriesCRC uint32) []byte {
	b := make([]byte, l.LogicalSectorSize)
	copy(b, "EFI PART")
	binary.LittleEndian.PutUint32(b[8:12], 0x00010000)
	binary.LittleEndian.PutUint32(b[12:16], HeaderSize)
	binary.LittleEndian.PutUint64(b[24:32], current)
	binary.LittleEndian.PutUint64(b[32:40], alternate)
	binary.LittleEndian.PutUint64(b[40:48], l.FirstUsableLBA)
	binary.LittleEndian.PutUint64(b[48:56], l.LastUsableLBA)
	copy(b[56:72], l.DiskGUID.MarshalBinary())
	binary.LittleEndian.PutUint64(b[72:80], entriesLBA)
	binary.LittleEndian.PutUint32(b[80:84], EntryCount)
	binary.LittleEndian.PutUint32(b[84:88], EntrySize)
	binary.LittleEndian.PutUint32(b[88:92], entriesCRC)
	binary.LittleEndian.PutUint32(b[16:20], crc32.ChecksumIEEE(b[:HeaderSize]))
	return b
}

func (l *Layout) Validate() error {
	if l == nil || !l.validMetadataLBAs() {
		return ErrInvalidLayout
	}
	if !l.validPartitionBounds() || !l.validHeaders() {
		return ErrInvalidLayout
	}
	return nil
}

// validMetadataLBAs checks that the entry arrays and usable region sit exactly
// where Build places them for this disk and sector size.
func (l *Layout) validMetadataLBAs() bool {
	if l.LogicalSectorSize < 512 || l.TotalLBAs < 2 {
		return false
	}
	if uint64(len(l.entries)) != entryArrayBytes {
		return false
	}
	entrySectors, ok := ceilDiv(entryArrayBytes, l.LogicalSectorSize)
	backupEntries := l.TotalLBAs - 1 - entrySectors
	want := [4]uint64{2, backupEntries, 2 + entrySectors, backupEntries - 1}
	got := [4]uint64{l.PrimaryEntriesLBA, l.BackupEntriesLBA, l.FirstUsableLBA, l.LastUsableLBA}
	return ok && got == want
}
func (l *Layout) validPartitionBounds() bool {
	if l.PartitionStartLBA > l.PartitionEndLBA {
		return false
	}
	return l.PartitionStartLBA >= l.FirstUsableLBA && l.PartitionEndLBA <= l.LastUsableLBA
}
func (l *Layout) validHeaders() bool {
	last := l.TotalLBAs - 1
	entriesCRC := crc32.ChecksumIEEE(l.entries)
	return validHeader(l.primaryHeader, 1, last, l.PrimaryEntriesLBA, entriesCRC) &&
		validHeader(l.backupHeader, last, 1, l.BackupEntriesLBA, entriesCRC)
}
func validHeader(header []byte, current, alternate, entriesLBA uint64, entriesCRC uint32) bool {
	if !validHeaderCRC(header) {
		return false
	}
	got := [3]uint64{binary.LittleEndian.Uint64(header[24:32]), binary.LittleEndian.Uint64(header[32:40]), binary.LittleEndian.Uint64(header[72:80])}
	return got == [3]uint64{current, alternate, entriesLBA} && binary.LittleEndian.Uint32(header[88:92]) == entriesCRC
}
func validHeaderCRC(header []byte) bool {
	if len(header) < int(HeaderSize) || binary.LittleEndian.Uint32(header[12:16]) != HeaderSize {
		return false
	}
	b := append([]byte(nil), header[:HeaderSize]...)
	want := binary.LittleEndian.Uint32(b[16:20])
	binary.LittleEndian.PutUint32(b[16:20], 0)
	return crc32.ChecksumIEEE(b) == want
}

func (l *Layout) WriteTo(w io.WriterAt) error {
	if w == nil {
		return ErrInvalidLayout
	}
	if err := l.Validate(); err != nil {
		return err
	}
	mbr := make([]byte, l.LogicalSectorSize)
	p := mbr[446:462]
	p[4] = 0xee
	binary.LittleEndian.PutUint32(p[8:12], 1)
	n := l.TotalLBAs - 1
	if n > math.MaxUint32 {
		n = math.MaxUint32
	}
	binary.LittleEndian.PutUint32(p[12:16], uint32(n))
	mbr[510] = 0x55
	mbr[511] = 0xaa
	for _, x := range []struct {
		lba uint64
		b   []byte
	}{{0, mbr}, {1, l.primaryHeader}, {l.PrimaryEntriesLBA, l.entries}, {l.BackupEntriesLBA, l.entries}, {l.TotalLBAs - 1, l.backupHeader}} {
		off, ok := mulInt64(x.lba, l.LogicalSectorSize)
		if !ok {
			return ErrInvalidLayout
		}
		if err := writeFullAt(w, x.b, off); err != nil {
			return err
		}
	}
	return nil
}
func writeFullAt(w io.WriterAt, p []byte, off int64) error {
	for len(p) > 0 {
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
func ceilDiv(a, b uint64) (uint64, bool) {
	if b == 0 {
		return 0, false
	}
	q := a / b
	if a%b != 0 {
		if q == math.MaxUint64 {
			return 0, false
		}
		q++
	}
	return q, true
}
func alignUp(v, a uint64) (uint64, bool) {
	if a == 0 {
		return 0, false
	}
	r := v % a
	if r == 0 {
		return v, true
	}
	d := a - r
	if v > math.MaxUint64-d {
		return 0, false
	}
	return v + d, true
}
func mul(a, b uint64) (uint64, bool) {
	if a != 0 && b > math.MaxUint64/a {
		return 0, false
	}
	return a * b, true
}

// mulInt64 multiplies a and b, failing when the product is not a valid int64
// byte offset.
func mulInt64(a, b uint64) (int64, bool) {
	p, ok := mul(a, b)
	if !ok || p > math.MaxInt64 {
		return 0, false
	}
	return int64(p), true
}

// rangeFits reports whether [off, off+n) lies within [0, limit] without
// overflowing.
func rangeFits(off, n, limit uint64) bool {
	return off <= limit && n <= limit-off
}

// PartitionWriterAt translates partition-relative offsets and rejects an
// invalid request in full before making any underlying write.
type PartitionWriterAt struct {
	w          io.WriterAt
	base, size uint64
}

func NewPartitionWriterAt(w io.WriterAt, startLBA, endLBA, sectorSize uint64) (*PartitionWriterAt, error) {
	if w == nil {
		return nil, ErrInvalidLayout
	}
	base, size, ok := partitionExtent(startLBA, endLBA, sectorSize)
	if !ok {
		return nil, ErrInvalidLayout
	}
	return &PartitionWriterAt{w, base, size}, nil
}

// partitionExtent converts the inclusive LBA range to a byte base and size
// whose end is still addressable as an int64 offset. The results are base,
// size, then ok.
func partitionExtent(startLBA, endLBA, sectorSize uint64) (uint64, uint64, bool) {
	if sectorSize == 0 || startLBA > endLBA {
		return 0, 0, false
	}
	span := endLBA - startLBA
	if span == math.MaxUint64 {
		return 0, 0, false
	}
	base, ok := mul(startLBA, sectorSize)
	if !ok {
		return 0, 0, false
	}
	size, ok := mul(span+1, sectorSize)
	if !ok {
		return 0, 0, false
	}
	return base, size, rangeFits(base, size, math.MaxInt64)
}
func (p *PartitionWriterAt) WriteAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, ErrInvalidLayout
	}
	u := uint64(off)
	if !rangeFits(u, uint64(len(b)), p.size) || !rangeFits(u, p.base, math.MaxInt64) {
		return 0, ErrInvalidLayout
	}
	return p.w.WriteAt(b, int64(p.base+u))
}

// Sync flushes the backing device when it supports syncing. This makes a
// partition view suitable for APIs which require both WriterAt and Sync while
// preserving support for in-memory WriterAt implementations.
func (p *PartitionWriterAt) Sync() error {
	if s, ok := p.w.(interface{ Sync() error }); ok {
		return s.Sync()
	}
	return nil
}
