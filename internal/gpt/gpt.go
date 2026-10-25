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
	primaryHeader, backupHeader headerSector
	entries                     []byte
}

// lba is a logical block address, or a count of logical blocks.
type lba uint64

// alignUp rounds l up to a multiple of a, failing on overflow or a zero a.
func (l lba) alignUp(a lba) (lba, bool) {
	if a == 0 {
		return 0, false
	}
	r := l % a
	if r == 0 {
		return l, true
	}
	d := a - r
	if l > math.MaxUint64-d {
		return 0, false
	}
	return l + d, true
}

// sectorSize is the logical sector size every LBA is measured in.
type sectorSize uint64

func (s sectorSize) valid() bool {
	return s >= 512 && s&(s-1) == 0
}

// entrySectors is how many sectors one partition entry array occupies.
func (s sectorSize) entrySectors() (lba, bool) {
	n, ok := ceilDiv(entryArrayBytes, uint64(s))
	return lba(n), ok
}

// alignedStart rounds firstUsable up to the 1 MiB partition alignment.
func (s sectorSize) alignedStart(firstUsable lba) (lba, bool) {
	alignLBAs, ok := ceilDiv(alignmentBytes, uint64(s))
	if !ok {
		return 0, false
	}
	return firstUsable.alignUp(lba(alignLBAs))
}

// offset is the byte offset of at, failing when it is not a valid int64.
func (s sectorSize) offset(at lba) (int64, bool) {
	return mulInt64(uint64(at), uint64(s))
}

// disk is the target's size in logical sectors.
type disk struct {
	total  lba
	sector sectorSize
}

// geometry is the LBA placement of the GPT metadata and the single partition.
type geometry struct {
	last, firstUsable, lastUsable, backupEntries, start lba
}

// Build creates one EFI System Partition. random may be nil to use crypto/rand.
func Build(totalLBAs, logicalSectorSize uint64, random io.Reader) (*Layout, error) {
	g, err := disk{total: lba(totalLBAs), sector: sectorSize(logicalSectorSize)}.plan()
	if err != nil {
		return nil, err
	}
	if random == nil {
		random = rand.Reader
	}
	l := &Layout{LogicalSectorSize: logicalSectorSize, TotalLBAs: totalLBAs, FirstUsableLBA: uint64(g.firstUsable), LastUsableLBA: uint64(g.lastUsable), PartitionStartLBA: uint64(g.start), PartitionEndLBA: uint64(g.lastUsable), PrimaryEntriesLBA: 2, BackupEntriesLBA: uint64(g.backupEntries), entries: make([]byte, entryArrayBytes)}
	if err := l.assignGUIDs(random); err != nil {
		return nil, err
	}
	l.encodePartitionEntry()
	entriesCRC := crc32.ChecksumIEEE(l.entries)
	l.primaryHeader = l.header(headerFields{1, uint64(g.last), l.PrimaryEntriesLBA, entriesCRC})
	l.backupHeader = l.header(headerFields{uint64(g.last), 1, l.BackupEntriesLBA, entriesCRC})
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return l, nil
}

// plan places the primary and backup metadata around a usable region and
// aligns the partition start to 1 MiB.
func (d disk) plan() (geometry, error) {
	if !d.sector.valid() {
		return geometry{}, ErrInvalidLayout
	}
	if _, ok := d.sector.offset(d.total); !ok {
		return geometry{}, ErrInvalidLayout
	}
	entrySectors, ok := d.sector.entrySectors()
	if !ok || !d.metadataFits(entrySectors) {
		return geometry{}, ErrInvalidLayout
	}
	g := geometry{last: d.total - 1, firstUsable: 2 + entrySectors}
	g.backupEntries = g.last - entrySectors
	g.lastUsable = g.backupEntries - 1
	start, ok := d.sector.alignedStart(g.firstUsable)
	g.start = start
	if !ok || g.start > g.lastUsable {
		return geometry{}, ErrInvalidLayout
	}
	return g, nil
}

// metadataFits reports whether the protective MBR, both headers and both entry
// arrays fit on the disk with at least one LBA left between the arrays.
func (d disk) metadataFits(entrySectors lba) bool {
	if d.total < 4 {
		return false
	}
	return entrySectors <= d.total-3
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
func (l *Layout) header(f headerFields) headerSector {
	b := make(headerSector, l.LogicalSectorSize)
	copy(b, "EFI PART")
	binary.LittleEndian.PutUint32(b[8:12], 0x00010000)
	binary.LittleEndian.PutUint32(b[12:16], HeaderSize)
	binary.LittleEndian.PutUint64(b[24:32], f.current)
	binary.LittleEndian.PutUint64(b[32:40], f.alternate)
	binary.LittleEndian.PutUint64(b[40:48], l.FirstUsableLBA)
	binary.LittleEndian.PutUint64(b[48:56], l.LastUsableLBA)
	copy(b[56:72], l.DiskGUID.MarshalBinary())
	binary.LittleEndian.PutUint64(b[72:80], f.entriesLBA)
	binary.LittleEndian.PutUint32(b[80:84], EntryCount)
	binary.LittleEndian.PutUint32(b[84:88], EntrySize)
	binary.LittleEndian.PutUint32(b[88:92], f.entriesCRC)
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
	entrySectors, ok := sectorSize(l.LogicalSectorSize).entrySectors()
	backupEntries := l.TotalLBAs - 1 - uint64(entrySectors)
	want := [4]uint64{2, backupEntries, 2 + uint64(entrySectors), backupEntries - 1}
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
	return l.primaryHeader.matches(headerFields{1, last, l.PrimaryEntriesLBA, entriesCRC}) &&
		l.backupHeader.matches(headerFields{last, 1, l.BackupEntriesLBA, entriesCRC})
}

// headerFields are the header values that differ between the primary and
// backup copies, plus the CRC both share over the entry array.
type headerFields struct {
	current, alternate, entriesLBA uint64
	entriesCRC                     uint32
}

// headerSector is one encoded GPT header, padded to a logical sector.
type headerSector []byte

// matches reports whether h has a valid CRC and carries want.
func (h headerSector) matches(want headerFields) bool {
	if !h.validCRC() {
		return false
	}
	got := headerFields{
		current:    binary.LittleEndian.Uint64(h[24:32]),
		alternate:  binary.LittleEndian.Uint64(h[32:40]),
		entriesLBA: binary.LittleEndian.Uint64(h[72:80]),
		entriesCRC: binary.LittleEndian.Uint32(h[88:92]),
	}
	return got == want
}

func (h headerSector) validCRC() bool {
	if len(h) < int(HeaderSize) || binary.LittleEndian.Uint32(h[12:16]) != HeaderSize {
		return false
	}
	b := append([]byte(nil), h[:HeaderSize]...)
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
	sector := sectorSize(l.LogicalSectorSize)
	for _, p := range []placement{{0, mbr}, {1, l.primaryHeader}, {lba(l.PrimaryEntriesLBA), l.entries}, {lba(l.BackupEntriesLBA), l.entries}, {lba(l.TotalLBAs - 1), l.backupHeader}} {
		if err := p.write(w, sector); err != nil {
			return err
		}
	}
	return nil
}

// placement is data destined for the sectors starting at at.
type placement struct {
	at   lba
	data []byte
}

// write stores p.data at its byte offset, continuing after short writes. An
// offset that is not a valid int64 is ErrInvalidLayout.
func (p placement) write(w io.WriterAt, s sectorSize) error {
	off, ok := s.offset(p.at)
	if !ok {
		return ErrInvalidLayout
	}
	data := p.data
	for len(data) > 0 {
		n, err := w.WriteAt(data, off)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
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

func NewPartitionWriterAt(w io.WriterAt, startLBA, endLBA, logicalSectorSize uint64) (*PartitionWriterAt, error) {
	if w == nil {
		return nil, ErrInvalidLayout
	}
	base, size, ok := partitionRange{start: lba(startLBA), end: lba(endLBA), sector: sectorSize(logicalSectorSize)}.extent()
	if !ok {
		return nil, ErrInvalidLayout
	}
	return &PartitionWriterAt{w, base, size}, nil
}

// partitionRange is an inclusive LBA range and the sector size it counts in.
type partitionRange struct {
	start, end lba
	sector     sectorSize
}

// extent converts r to a byte base and size whose end is still addressable as
// an int64 offset. The results are base, size, then ok.
func (r partitionRange) extent() (uint64, uint64, bool) {
	if r.sector == 0 || r.start > r.end {
		return 0, 0, false
	}
	span := r.end - r.start
	if span == math.MaxUint64 {
		return 0, 0, false
	}
	base, ok := mul(uint64(r.start), uint64(r.sector))
	if !ok {
		return 0, 0, false
	}
	size, ok := mul(uint64(span+1), uint64(r.sector))
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
