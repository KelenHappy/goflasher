// Package iso provides the bounded, read-only random-access filesystem view
// used by the Windows installer workflow.
package iso

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf16"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	sectorSize = int64(2048)
	maxEntries = 200000
	maxDepth   = 64
	maxRead    = 64 << 20
)

var ErrInvalidImage = errors.New("invalid or unsafe installer ISO")

type EntryType string

const (
	File      EntryType = "file"
	Directory EntryType = "directory"
)

type Extent struct{ Offset, Length uint64 }
type Entry struct {
	Path               string
	Type               EntryType
	Extents            []Extent
	Size               uint64
	DestinationFATPath string
}
type Manifest struct{ Entries []Entry }

type Reader struct {
	source   io.ReaderAt
	size     uint64
	retained io.Closer
	manifest Manifest
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidImage, fmt.Sprintf(format, args...))
}

// New completely parses and validates the immutable manifest before returning.
// retained pins the caller's source object for both parsing and later extraction.
func New(source io.ReaderAt, size int64, retained io.Closer) (*Reader, error) {
	if source == nil || retained == nil || size < 0 {
		return nil, invalid("invalid source")
	}
	r := &Reader{source: source, size: uint64(size), retained: retained}
	entries, err := r.parse()
	if err != nil {
		_ = retained.Close()
		return nil, err
	}
	r.manifest.Entries = entries
	return r, nil
}

func (r *Reader) parse() ([]Entry, error) {
	entries, found, err := r.readISO9660()
	if err != nil {
		return nil, err
	}
	if !found {
		if entries, err = r.readUDF(); err != nil {
			return nil, err
		}
	}
	if err := r.validate(entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (r *Reader) Close() error { return r.retained.Close() }
func (r *Reader) Manifest() Manifest {
	out := Manifest{Entries: make([]Entry, len(r.manifest.Entries))}
	for i, e := range r.manifest.Entries {
		out.Entries[i] = e
		out.Entries[i].Extents = append([]Extent(nil), e.Extents...)
	}
	return out
}
func (r *Reader) ReadAt(p []byte, off int64) (int, error) { return r.source.ReadAt(p, off) }

func (r *Reader) bytes(off, length uint64) ([]byte, error) {
	if off > r.size || length > r.size-off || length > maxRead {
		return nil, invalid("extent out of bounds")
	}
	b := make([]byte, length)
	if _, err := r.source.ReadAt(b, int64(off)); err != nil {
		return nil, invalid("truncated extent: %v", err)
	}
	return b, nil
}

func (r *Reader) sector(n uint64) ([]byte, error) {
	return r.bytes(n*uint64(sectorSize), uint64(sectorSize))
}

// readISO9660 scans the volume descriptor set. Joliet is preferred because it
// preserves the names Windows media actually uses.
func (r *Reader) readISO9660() ([]Entry, bool, error) {
	var all []Entry
	found := false
	for sec := uint64(16); sec < 256; sec++ {
		b, err := r.sector(sec)
		if err != nil {
			return nil, found, err
		}
		if !isVolumeDescriptor(b) {
			continue
		}
		if b[0] == 255 {
			break
		}
		joliet, usable := usableVolumeDescriptor(b)
		if !usable {
			continue
		}
		found = true
		entries, err := r.walkVolumeDescriptor(b, joliet)
		if err != nil {
			return nil, true, err
		}
		if joliet || len(all) == 0 {
			all = entries
		}
	}
	return all, found, nil
}

func isVolumeDescriptor(b []byte) bool { return string(b[1:6]) == "CD001" }

// usableVolumeDescriptor reports whether b is a primary descriptor or a
// Joliet supplementary descriptor (UCS-2 escape sequence), and which.
func usableVolumeDescriptor(b []byte) (joliet, usable bool) {
	if b[0] == 1 {
		return false, true
	}
	if b[0] == 2 && b[88] == 0x25 && b[89] == 0x2f {
		return true, true
	}
	return false, false
}

func (r *Reader) walkVolumeDescriptor(b []byte, joliet bool) ([]Entry, error) {
	w := &isoWalker{r: r, joliet: joliet, seen: map[uint32]bool{}}
	if err := w.walk(b[156:190], "", 0); err != nil {
		return nil, err
	}
	return w.out, nil
}

// udfLayout is what the volume descriptor sequence contributes: the physical
// partition start and the file set descriptor location within it.
type udfLayout struct {
	partitionStart uint32
	fsdBlock       uint32
}

// readUDF implements the read-only ECMA-167 subset used by Windows mastering
// media (UDF 1.02/2.x, one physical partition, short/long allocation
// descriptors). Unsupported allocation forms fail closed.
func (r *Reader) readUDF() ([]Entry, error) {
	seq, err := r.readUDFDescriptorSequence()
	if err != nil {
		return nil, err
	}
	layout, err := parseUDFDescriptorSequence(seq)
	if err != nil {
		return nil, err
	}
	root, err := r.udfRootBlock(layout)
	if err != nil {
		return nil, err
	}
	w := &udfWalker{r: r, part: layout.partitionStart, seen: map[uint32]bool{}}
	if err := w.walk(root, "", 0); err != nil {
		return nil, err
	}
	return w.out, nil
}

func (r *Reader) readUDFDescriptorSequence() ([]byte, error) {
	anchor, err := r.sector(256)
	if err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint16(anchor[:2]) != 2 {
		return nil, invalid("no filesystem descriptor")
	}
	seqLen, seqBlock := binary.LittleEndian.Uint32(anchor[16:20]), binary.LittleEndian.Uint32(anchor[20:24])
	if seqLen == 0 {
		return nil, invalid("empty descriptor sequence")
	}
	return r.bytes(uint64(seqBlock)*uint64(sectorSize), uint64(seqLen))
}

func parseUDFDescriptorSequence(seq []byte) (udfLayout, error) {
	var layout udfLayout
	havePartition, haveLVD := false, false
	for off := 0; off+512 <= len(seq); off += int(sectorSize) {
		d := seq[off:]
		switch binary.LittleEndian.Uint16(d[:2]) {
		case 5:
			layout.partitionStart = binary.LittleEndian.Uint32(d[188:192])
			havePartition = true
		case 6:
			if binary.LittleEndian.Uint32(d[212:216]) != uint32(sectorSize) {
				return udfLayout{}, invalid("unsupported UDF block size")
			}
			layout.fsdBlock = binary.LittleEndian.Uint32(d[252:256])
			haveLVD = true
		case 8:
			off = len(seq)
		}
	}
	if !havePartition || !haveLVD {
		return udfLayout{}, invalid("incomplete UDF descriptors")
	}
	return layout, nil
}

func (r *Reader) udfRootBlock(layout udfLayout) (uint32, error) {
	fsd, err := r.udfBlock(layout.partitionStart, layout.fsdBlock)
	if err != nil {
		return 0, err
	}
	if binary.LittleEndian.Uint16(fsd[:2]) != 256 {
		return 0, invalid("missing file set descriptor")
	}
	return binary.LittleEndian.Uint32(fsd[404:408]), nil
}

func (r *Reader) udfBlock(part, block uint32) ([]byte, error) {
	off := uint64(part) + uint64(block)
	if off > ^uint64(0)/uint64(sectorSize) {
		return nil, invalid("UDF block overflow")
	}
	return r.sector(off)
}

const (
	udfFileTypeDirectory = 4
	udfFileTypeSymlink   = 12
)

// udfFileEntry is the parsed common subset of a File Entry (tag 261) and an
// Extended File Entry (tag 266).
type udfFileEntry struct {
	fileType  byte
	infoLen   uint64
	allocType uint16
	allocs    []byte
}

func (e udfFileEntry) isDir() bool { return e.fileType == udfFileTypeDirectory }

func parseUDFFileEntry(b []byte) (udfFileEntry, error) {
	var infoLen uint64
	var ea, ad uint32
	var start int
	switch binary.LittleEndian.Uint16(b[:2]) {
	case 261:
		infoLen = binary.LittleEndian.Uint64(b[56:64])
		ea, ad = binary.LittleEndian.Uint32(b[168:172]), binary.LittleEndian.Uint32(b[172:176])
		start = 176
	case 266:
		infoLen = binary.LittleEndian.Uint64(b[64:72])
		ea, ad = binary.LittleEndian.Uint32(b[208:212]), binary.LittleEndian.Uint32(b[212:216])
		start = 216
	default:
		return udfFileEntry{}, invalid("unsupported UDF file entry")
	}
	if b[27] == udfFileTypeSymlink {
		return udfFileEntry{}, invalid("symlink-like entry")
	}
	if uint64(start)+uint64(ea)+uint64(ad) > uint64(len(b)) {
		return udfFileEntry{}, invalid("allocation descriptors")
	}
	allocStart := start + int(ea)
	return udfFileEntry{
		fileType:  b[27],
		infoLen:   infoLen,
		allocType: binary.LittleEndian.Uint16(b[34:36]) & 7,
		allocs:    b[allocStart : allocStart+int(ad)],
	}, nil
}

func (r *Reader) udfFileEntry(part, block uint32) (udfFileEntry, error) {
	b, err := r.udfBlock(part, block)
	if err != nil {
		return udfFileEntry{}, err
	}
	return parseUDFFileEntry(b)
}

type udfWalker struct {
	r    *Reader
	part uint32
	seen map[uint32]bool
	out  []Entry
}

func (w *udfWalker) walk(block uint32, prefix string, depth int) error {
	if depth > maxDepth {
		return invalid("directory depth")
	}
	if w.seen[block] {
		return invalid("directory cycle")
	}
	w.seen[block] = true
	defer delete(w.seen, block)
	fe, err := w.r.udfFileEntry(w.part, block)
	if err != nil {
		return err
	}
	exts, err := udfExtents(w.part, fe.allocs, fe.allocType)
	if err != nil {
		return err
	}
	if !fe.isDir() {
		return invalid("root is not directory")
	}
	data, err := w.r.readExtents(exts, fe.infoLen)
	if err != nil {
		return err
	}
	fids, err := parseUDFFileIdentifiers(data)
	if err != nil {
		return err
	}
	for _, fid := range fids {
		if err := w.addChild(fid, prefix, depth); err != nil {
			return err
		}
	}
	return nil
}

// udfFID is a File Identifier Descriptor (tag 257) reduced to what the walk
// needs.
type udfFID struct {
	name  []byte
	child uint32
}

// parseUDFFileIdentifiers splits a directory's data into identifiers,
// dropping deleted entries and parent links (flag bits 2 and 3).
func parseUDFFileIdentifiers(data []byte) ([]udfFID, error) {
	var out []udfFID
	for pos := 0; pos+38 <= len(data); {
		if binary.LittleEndian.Uint16(data[pos:pos+2]) != 257 {
			return nil, invalid("malformed file identifier")
		}
		flags := data[pos+18]
		nameLen := int(data[pos+19])
		implLen := int(binary.LittleEndian.Uint16(data[pos+36 : pos+38]))
		n := (38 + implLen + nameLen + 3) &^ 3
		if n <= 0 || pos+n > len(data) {
			return nil, invalid("truncated file identifier")
		}
		fid := udfFID{name: data[pos+38+implLen : pos+38+implLen+nameLen], child: binary.LittleEndian.Uint32(data[pos+24 : pos+28])}
		pos += n
		if flags&0x0c != 0 {
			continue
		}
		out = append(out, fid)
	}
	return out, nil
}

func (w *udfWalker) addChild(fid udfFID, prefix string, depth int) error {
	name, err := decodeOSTA(fid.name)
	if err != nil {
		return err
	}
	p, err := normalize(prefix, name)
	if err != nil {
		return err
	}
	ce, err := w.r.udfEntry(w.part, fid.child, p)
	if err != nil {
		return err
	}
	w.out = append(w.out, ce)
	if len(w.out) > maxEntries {
		return invalid("too many entries")
	}
	if ce.Type == Directory {
		return w.walk(fid.child, p, depth+1)
	}
	return nil
}

func (r *Reader) udfEntry(part, block uint32, p string) (Entry, error) {
	fe, err := r.udfFileEntry(part, block)
	if err != nil {
		return Entry{}, err
	}
	ex, err := udfExtents(part, fe.allocs, fe.allocType)
	if err != nil {
		return Entry{}, err
	}
	typ := File
	if fe.isDir() {
		typ = Directory
	}
	return Entry{Path: p, Type: typ, Extents: ex, Size: fe.infoLen, DestinationFATPath: fatPath(p)}, nil
}

func udfExtents(part uint32, b []byte, t uint16) ([]Extent, error) {
	var step, posoff int
	switch t {
	case 0:
		step, posoff = 8, 4
	case 1:
		step, posoff = 16, 4
	case 3:
		return nil, invalid("embedded UDF data unsupported")
	default:
		return nil, invalid("unsupported UDF allocation")
	}
	var out []Extent
	for len(b) >= step {
		raw := binary.LittleEndian.Uint32(b[:4])
		length := uint64(raw & 0x3fffffff)
		block := binary.LittleEndian.Uint32(b[posoff : posoff+4])
		offBlocks := uint64(part) + uint64(block)
		if offBlocks > ^uint64(0)/uint64(sectorSize) {
			return nil, invalid("extent overflow")
		}
		out = append(out, Extent{offBlocks * uint64(sectorSize), length})
		b = b[step:]
	}
	return out, nil
}

func (r *Reader) readExtents(es []Extent, size uint64) ([]byte, error) {
	if size > maxRead {
		return nil, invalid("directory too large")
	}
	out := make([]byte, 0, size)
	for _, e := range es {
		n := min(e.Length, size-uint64(len(out)))
		b, err := r.bytes(e.Offset, n)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
		if uint64(len(out)) == size {
			break
		}
	}
	if uint64(len(out)) != size {
		return nil, invalid("short directory")
	}
	return out, nil
}

func decodeOSTA(b []byte) (string, error) {
	if len(b) == 0 {
		return "", invalid("empty UDF name")
	}
	switch b[0] {
	case 8:
		return string(b[1:]), nil
	case 16:
		return decodeUCS2BE(b[1:]), nil
	default:
		return "", invalid("UDF name encoding")
	}
}

func decodeUCS2BE(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for len(b) >= 2 {
		u = append(u, binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	return string(utf16.Decode(u))
}

type isoWalker struct {
	r      *Reader
	joliet bool
	seen   map[uint32]bool
	out    []Entry
}

func (w *isoWalker) walk(rec []byte, prefix string, depth int) error {
	if depth > maxDepth {
		return invalid("directory depth")
	}
	if len(rec) < 34 {
		return invalid("directory record")
	}
	ext, size := binary.LittleEndian.Uint32(rec[2:6]), binary.LittleEndian.Uint32(rec[10:14])
	if w.seen[ext] {
		return invalid("directory cycle")
	}
	w.seen[ext] = true
	defer delete(w.seen, ext)
	b, err := w.r.bytes(uint64(ext)*uint64(sectorSize), uint64(size))
	if err != nil {
		return err
	}
	records, err := isoDirectoryRecords(b)
	if err != nil {
		return err
	}
	pending := map[string]int{}
	for _, x := range records {
		if err := w.addRecord(x, prefix, depth, pending); err != nil {
			return err
		}
	}
	if len(pending) != 0 {
		return invalid("unterminated multi-extent file")
	}
	return nil
}

// isoDirectoryRecords splits directory data into records. A zero length byte
// marks sector padding; the next record begins at the following sector.
func isoDirectoryRecords(b []byte) ([][]byte, error) {
	var out [][]byte
	for pos := 0; pos < len(b); {
		n := int(b[pos])
		if n == 0 {
			pos = ((pos / int(sectorSize)) + 1) * int(sectorSize)
			continue
		}
		if n < 34 || pos+n > len(b) {
			return nil, invalid("malformed directory")
		}
		out = append(out, b[pos:pos+n])
		pos += n
	}
	return out, nil
}

// isoRecordName decodes the record's file identifier. skip reports the "."
// and ".." entries, which are encoded as a single 0x00 or 0x01 byte.
func isoRecordName(x []byte, joliet bool) (name string, skip bool, err error) {
	nl := int(x[32])
	if 33+nl > len(x) {
		return "", false, invalid("malformed name")
	}
	nb := x[33 : 33+nl]
	if nl == 1 && (nb[0] == 0 || nb[0] == 1) {
		return "", true, nil
	}
	return strings.TrimSuffix(decodeName(nb, joliet), ";1"), false, nil
}

func isoEntry(x []byte, p string) Entry {
	e := Entry{Path: p, Type: File, Size: uint64(binary.LittleEndian.Uint32(x[10:14])), DestinationFATPath: fatPath(p)}
	e.Extents = []Extent{{Offset: uint64(binary.LittleEndian.Uint32(x[2:6])) * uint64(sectorSize), Length: e.Size}}
	if x[25]&2 != 0 {
		e.Type = Directory
	}
	return e
}

func isoRecordContinues(x []byte) bool { return x[25]&0x80 != 0 }

func (w *isoWalker) addRecord(x []byte, prefix string, depth int, pending map[string]int) error {
	name, skip, err := isoRecordName(x, w.joliet)
	if err != nil || skip {
		return err
	}
	p, err := normalize(prefix, name)
	if err != nil {
		return err
	}
	e := isoEntry(x, p)
	if idx, ok := pending[p]; ok && e.Type == File {
		return w.mergeExtent(idx, e, isoRecordContinues(x), pending)
	}
	w.out = append(w.out, e)
	if e.Type == File && isoRecordContinues(x) {
		pending[p] = len(w.out) - 1
	}
	if len(w.out) > maxEntries {
		return invalid("too many entries")
	}
	if e.Type == Directory {
		return w.walk(x, p, depth+1)
	}
	return nil
}

// mergeExtent appends a continuation record of a multi-extent file to the
// entry already collected for it.
func (w *isoWalker) mergeExtent(idx int, e Entry, continues bool, pending map[string]int) error {
	target := &w.out[idx]
	if target.Size > ^uint64(0)-e.Size {
		return invalid("multi-extent overflow")
	}
	target.Extents = append(target.Extents, e.Extents...)
	target.Size += e.Size
	if !continues {
		delete(pending, e.Path)
	}
	return nil
}

func decodeName(b []byte, joliet bool) string {
	if !joliet {
		return string(b)
	}
	return decodeUCS2BE(b)
}

func normalize(parent, name string) (string, error) {
	if unsafeName(name) {
		return "", invalid("unsafe path")
	}
	p := name
	if parent != "" {
		p = parent + "/" + name
	}
	p = path.Clean(p)
	if escapesRoot(p) {
		return "", invalid("traversal")
	}
	return p, nil
}

func unsafeName(name string) bool {
	if name == "." || name == ".." || path.IsAbs(name) {
		return true
	}
	return strings.ContainsAny(name, "\x00/\\")
}

func escapesRoot(p string) bool {
	return p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/")
}

func fatPath(p string) string { return strings.ReplaceAll(p, "/", "\\") }

// extentSpan is one occupied byte range and the path that owns it.
type extentSpan struct {
	start, end uint64
	path       string
}

type validator struct {
	size  uint64
	paths map[string]bool
	fold  map[string]string
	spans []extentSpan
}

func (r *Reader) validate(es []Entry) error {
	v := &validator{size: r.size, paths: map[string]bool{}, fold: map[string]string{}}
	for _, e := range es {
		if err := v.checkEntry(e); err != nil {
			return err
		}
	}
	return v.checkSpans()
}

func (v *validator) checkEntry(e Entry) error {
	if err := v.checkPath(e.Path); err != nil {
		return err
	}
	if e.Type != File && e.Type != Directory {
		return invalid("symlink-like entry")
	}
	total, err := v.collectExtents(e)
	if err != nil {
		return err
	}
	if e.Type == File && total < e.Size {
		return invalid("short extent")
	}
	return nil
}

// checkPath rejects exact duplicates and names that collide once case and
// Unicode normalization are folded, since FAT32 cannot tell them apart.
func (v *validator) checkPath(p string) error {
	if p == "" || v.paths[p] {
		return invalid("duplicate path")
	}
	v.paths[p] = true
	k := norm.NFC.String(cases.Fold().String(p))
	if old, ok := v.fold[k]; ok && old != p {
		return invalid("case or Unicode collision")
	}
	v.fold[k] = p
	return nil
}

func (v *validator) collectExtents(e Entry) (uint64, error) {
	var total uint64
	for _, x := range e.Extents {
		if !v.inBounds(x) || total > x.Length+total {
			return 0, invalid("extent overflow")
		}
		total += x.Length
		if x.Length > 0 {
			v.spans = append(v.spans, extentSpan{x.Offset, x.Offset + x.Length, e.Path})
		}
	}
	return total, nil
}

func (v *validator) inBounds(x Extent) bool {
	return x.Offset <= v.size && x.Length <= v.size-x.Offset
}

// checkSpans rejects two paths claiming overlapping bytes; a single path may
// legitimately list adjacent or repeated extents.
func (v *validator) checkSpans() error {
	sort.Slice(v.spans, func(i, j int) bool { return v.spans[i].start < v.spans[j].start })
	for i := 1; i < len(v.spans); i++ {
		prev, cur := v.spans[i-1], v.spans[i]
		if cur.start < prev.end && cur.path != prev.path {
			return invalid("extent collision")
		}
	}
	return nil
}
