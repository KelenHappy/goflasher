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
	EntryCount     uint32 = 128
	EntrySize      uint32 = 128
	HeaderSize     uint32 = 92
	alignmentBytes uint64 = 1 << 20
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

// Build creates one EFI System Partition. random may be nil to use crypto/rand.
func Build(totalLBAs, logicalSectorSize uint64, random io.Reader) (*Layout, error) {
	firstUsable, lastUsable, backupEntries, start, ok := layoutGeometry(totalLBAs, logicalSectorSize)
	if !ok {
		return nil, ErrInvalidLayout
	}
	if random == nil {
		random = rand.Reader
	}
	l := &Layout{LogicalSectorSize: logicalSectorSize, TotalLBAs: totalLBAs, FirstUsableLBA: firstUsable, LastUsableLBA: lastUsable, PartitionStartLBA: start, PartitionEndLBA: lastUsable, PrimaryEntriesLBA: 2, BackupEntriesLBA: backupEntries, entries: make([]byte, uint64(EntryCount)*uint64(EntrySize))}
	if err := fillGUID(random, &l.DiskGUID); err != nil {
		return nil, err
	}
	if err := fillGUID(random, &l.PartitionGUID); err != nil {
		return nil, err
	}
	if l.DiskGUID == l.PartitionGUID {
		return nil, ErrInvalidLayout
	}
	l.buildPartitionEntry()
	entriesCRC := crc32.ChecksumIEEE(l.entries)
	last := totalLBAs - 1
	l.primaryHeader = l.header(1, last, l.PrimaryEntriesLBA, entriesCRC)
	l.backupHeader = l.header(last, 1, l.BackupEntriesLBA, entriesCRC)
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return l, nil
}

func layoutGeometry(totalLBAs, sectorSize uint64) (first, last, backup, start uint64, ok bool) {
	if sectorSize < 512 || sectorSize&(sectorSize-1) != 0 {
		return 0, 0, 0, 0, false
	}
	totalBytes, valid := mul(totalLBAs, sectorSize)
	entrySectors, entriesValid := ceilDiv(uint64(EntryCount)*uint64(EntrySize), sectorSize)
	if !valid || !entriesValid {
		return 0, 0, 0, 0, false
	}
	if totalBytes > math.MaxInt64 || totalLBAs < 4 {
		return 0, 0, 0, 0, false
	}
	if entrySectors > totalLBAs-3 {
		return 0, 0, 0, 0, false
	}
	backup = totalLBAs - 1 - entrySectors
	if backup == 0 {
		return 0, 0, 0, 0, false
	}
	first, last = 2+entrySectors, backup-1
	alignment, valid := ceilDiv(alignmentBytes, sectorSize)
	start, ok = alignUp(first, alignment)
	if !valid || !ok {
		return 0, 0, 0, 0, false
	}
	return first, last, backup, start, alignment != 0 && start <= last
}

func (l *Layout) buildPartitionEntry() {
	copy(l.entries[0:16], EFITypeGUID.MarshalBinary())
	copy(l.entries[16:32], l.PartitionGUID.MarshalBinary())
	binary.LittleEndian.PutUint64(l.entries[32:40], l.PartitionStartLBA)
	binary.LittleEndian.PutUint64(l.entries[40:48], l.PartitionEndLBA)
	for i, v := range utf16.Encode([]rune("EFI System Partition")) {
		binary.LittleEndian.PutUint16(l.entries[56+i*2:], v)
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
	if !l.validGeometry() || !l.validHeaders() || !l.validChecksums() {
		return ErrInvalidLayout
	}
	return nil
}

func (l *Layout) validGeometry() bool {
	if l == nil || l.LogicalSectorSize < 512 || l.TotalLBAs < 2 || len(l.entries) != int(EntryCount*EntrySize) {
		return false
	}
	last := l.TotalLBAs - 1
	entrySectors, ok := ceilDiv(uint64(len(l.entries)), l.LogicalSectorSize)
	if !ok || l.PrimaryEntriesLBA != 2 {
		return false
	}
	if l.BackupEntriesLBA+entrySectors != last || l.FirstUsableLBA != 2+entrySectors {
		return false
	}
	if l.LastUsableLBA+1 != l.BackupEntriesLBA || l.PartitionStartLBA < l.FirstUsableLBA {
		return false
	}
	return l.PartitionEndLBA <= l.LastUsableLBA && l.PartitionStartLBA <= l.PartitionEndLBA
}

func (l *Layout) validHeaders() bool {
	if len(l.primaryHeader) < int(HeaderSize) || len(l.backupHeader) < int(HeaderSize) {
		return false
	}
	last := l.TotalLBAs - 1
	if binary.LittleEndian.Uint64(l.primaryHeader[24:32]) != 1 || binary.LittleEndian.Uint64(l.primaryHeader[32:40]) != last {
		return false
	}
	if binary.LittleEndian.Uint64(l.backupHeader[24:32]) != last || binary.LittleEndian.Uint64(l.backupHeader[32:40]) != 1 {
		return false
	}
	return binary.LittleEndian.Uint64(l.primaryHeader[72:80]) == l.PrimaryEntriesLBA && binary.LittleEndian.Uint64(l.backupHeader[72:80]) == l.BackupEntriesLBA
}

func (l *Layout) validChecksums() bool {
	crc := crc32.ChecksumIEEE(l.entries)
	if !validHeaderCRC(l.primaryHeader) || !validHeaderCRC(l.backupHeader) {
		return false
	}
	return binary.LittleEndian.Uint32(l.primaryHeader[88:92]) == crc && binary.LittleEndian.Uint32(l.backupHeader[88:92]) == crc
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
	mbr[510], mbr[511] = 0x55, 0xaa
	for _, x := range []struct {
		lba uint64
		b   []byte
	}{{0, mbr}, {1, l.primaryHeader}, {l.PrimaryEntriesLBA, l.entries}, {l.BackupEntriesLBA, l.entries}, {l.TotalLBAs - 1, l.backupHeader}} {
		off, ok := mul(x.lba, l.LogicalSectorSize)
		if !ok || off > math.MaxInt64 {
			return ErrInvalidLayout
		}
		if err := writeFullAt(w, x.b, int64(off)); err != nil {
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

// PartitionWriterAt translates partition-relative offsets and rejects an
// invalid request in full before making any underlying write.
type PartitionWriterAt struct {
	w          io.WriterAt
	base, size uint64
}

func NewPartitionWriterAt(w io.WriterAt, startLBA, endLBA, sectorSize uint64) (*PartitionWriterAt, error) {
	if w == nil || sectorSize == 0 || startLBA > endLBA {
		return nil, ErrInvalidLayout
	}
	base, baseOK := mul(startLBA, sectorSize)
	span := endLBA - startLBA
	size, sizeOK := mul(span+1, sectorSize)
	if !baseOK || span == math.MaxUint64 {
		return nil, ErrInvalidLayout
	}
	if !sizeOK || base > math.MaxInt64 {
		return nil, ErrInvalidLayout
	}
	if size > uint64(math.MaxInt64)-base {
		return nil, ErrInvalidLayout
	}
	return &PartitionWriterAt{w, base, size}, nil
}
func (p *PartitionWriterAt) WriteAt(b []byte, off int64) (int, error) {
	if !p.validWrite(uint64(len(b)), off) {
		return 0, ErrInvalidLayout
	}
	u := uint64(off)
	return p.w.WriteAt(b, int64(p.base+u))
}

func (p *PartitionWriterAt) validWrite(length uint64, off int64) bool {
	if off < 0 {
		return false
	}
	u := uint64(off)
	if u > p.size || length > p.size-u {
		return false
	}
	if u > math.MaxInt64 {
		return false
	}
	return p.base <= uint64(math.MaxInt64)-u
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
