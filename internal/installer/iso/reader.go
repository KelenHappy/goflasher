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

type usedExtent struct {
	start, end uint64
	path       string
}

type udfMetadata struct {
	size                 uint64
	extended, allocation uint32
	start                int
	typeCode             byte
	allocationType       uint16
}

type udfChild struct {
	block uint32
	name  string
}

type isoWalker struct {
	reader *Reader
	joliet bool
	seen   map[uint32]bool
	out    *[]Entry
}

type Reader struct {
	source   io.ReaderAt
	size     uint64
	retained io.Closer
	manifest Manifest
}

// New completely parses and validates the immutable manifest before returning.
// retained pins the caller's source object for both parsing and later extraction.
func New(source io.ReaderAt, size int64, retained io.Closer) (*Reader, error) {
	if source == nil || retained == nil || size < 0 {
		return nil, fmt.Errorf("%w: invalid source", ErrInvalidImage)
	}
	r := &Reader{source: source, size: uint64(size), retained: retained}
	entries, found, err := r.readISO9660()
	if err != nil {
		_ = retained.Close()
		return nil, err
	}
	if !found {
		entries, err = r.readUDF()
		if err != nil {
			_ = retained.Close()
			return nil, err
		}
	}
	if err := r.validate(entries); err != nil {
		_ = retained.Close()
		return nil, err
	}
	r.manifest.Entries = entries
	return r, nil
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
	if off > r.size || length > r.size-off || length > 64<<20 {
		return nil, fmt.Errorf("%w: extent out of bounds", ErrInvalidImage)
	}
	b := make([]byte, length)
	if _, err := r.source.ReadAt(b, int64(off)); err != nil {
		return nil, fmt.Errorf("%w: truncated extent: %v", ErrInvalidImage, err)
	}
	return b, nil
}

func (r *Reader) readISO9660() ([]Entry, bool, error) {
	var all []Entry
	found := false
	for sec := uint64(16); sec < 256; sec++ {
		b, err := r.bytes(sec*uint64(sectorSize), uint64(sectorSize))
		if err != nil {
			return nil, found, err
		}
		if string(b[1:6]) != "CD001" {
			continue
		}
		if b[0] == 255 {
			break
		}
		if b[0] != 1 && !(b[0] == 2 && b[88] == 0x25 && b[89] == 0x2f) {
			continue
		}
		found = true
		joliet := b[0] == 2
		var entries []Entry
		walker := isoWalker{r, joliet, map[uint32]bool{}, &entries}
		if err := walker.walk(b[156:190], "", 0); err != nil {
			return nil, true, err
		}
		// Prefer Joliet because it preserves the names Windows media actually uses.
		if joliet || len(all) == 0 {
			all = entries
		}
	}
	return all, found, nil
}

// readUDF implements the read-only ECMA-167 subset used by Windows mastering
// media (UDF 1.02/2.x, one physical partition, short/long allocation
// descriptors). Unsupported allocation forms fail closed.
func (r *Reader) readUDF() ([]Entry, error) {
	anchor, err := r.bytes(256*uint64(sectorSize), uint64(sectorSize))
	if err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint16(anchor[:2]) != 2 {
		return nil, fmt.Errorf("%w: no filesystem descriptor", ErrInvalidImage)
	}
	seqLen, seqBlock := binary.LittleEndian.Uint32(anchor[16:20]), binary.LittleEndian.Uint32(anchor[20:24])
	if seqLen == 0 {
		return nil, fmt.Errorf("%w: empty descriptor sequence", ErrInvalidImage)
	}
	seq, err := r.bytes(uint64(seqBlock)*uint64(sectorSize), uint64(seqLen))
	if err != nil {
		return nil, err
	}
	var partitionStart uint32
	var fsdBlock uint32
	havePartition, haveLVD := false, false
	for off := 0; off+512 <= len(seq); off += int(sectorSize) {
		d := seq[off:]
		tag := binary.LittleEndian.Uint16(d[:2])
		switch tag {
		case 5:
			partitionStart = binary.LittleEndian.Uint32(d[188:192])
			havePartition = true
		case 6:
			if binary.LittleEndian.Uint32(d[212:216]) != uint32(sectorSize) {
				return nil, fmt.Errorf("%w: unsupported UDF block size", ErrInvalidImage)
			}
			fsdBlock = binary.LittleEndian.Uint32(d[252:256])
			haveLVD = true
		case 8:
			off = len(seq)
		}
	}
	if !havePartition || !haveLVD {
		return nil, fmt.Errorf("%w: incomplete UDF descriptors", ErrInvalidImage)
	}
	fsd, err := r.udfBlock(partitionStart, fsdBlock)
	if err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint16(fsd[:2]) != 256 {
		return nil, fmt.Errorf("%w: missing file set descriptor", ErrInvalidImage)
	}
	root := binary.LittleEndian.Uint32(fsd[404:408])
	var out []Entry
	if err := r.walkUDF(partitionStart, root, "", 0, map[uint32]bool{}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Reader) udfBlock(part, block uint32) ([]byte, error) {
	off := uint64(part) + uint64(block)
	if off > ^uint64(0)/uint64(sectorSize) {
		return nil, fmt.Errorf("%w: UDF block overflow", ErrInvalidImage)
	}
	return r.bytes(off*uint64(sectorSize), uint64(sectorSize))
}
func (r *Reader) walkUDF(part, block uint32, prefix string, depth int, seen map[uint32]bool, out *[]Entry) error {
	if depth > maxDepth {
		return fmt.Errorf("%w: directory depth", ErrInvalidImage)
	}
	if seen[block] {
		return fmt.Errorf("%w: directory cycle", ErrInvalidImage)
	}
	seen[block] = true
	defer delete(seen, block)
	fe, err := r.udfBlock(part, block)
	if err != nil {
		return err
	}
	metadata, err := parseUDFMetadata(fe)
	if err != nil {
		return err
	}
	if metadata.typeCode == 12 {
		return fmt.Errorf("%w: symlink-like entry", ErrInvalidImage)
	}
	exts, err := r.udfExtents(part, metadata.descriptors(fe), metadata.allocationType)
	if err != nil {
		return err
	}
	if metadata.typeCode != 4 {
		return fmt.Errorf("%w: root is not directory", ErrInvalidImage)
	}
	data, err := r.readExtents(exts, metadata.size)
	if err != nil {
		return err
	}
	for pos := 0; pos+38 <= len(data); {
		child, next, skip, err := parseUDFChild(data, pos)
		if err != nil {
			return err
		}
		pos = next
		if skip {
			continue
		}
		p, err := normalize(prefix, child.name)
		if err != nil {
			return err
		}
		childFE, err := r.udfBlock(part, child.block)
		if err != nil {
			return err
		}
		typ := childFE[27]
		if typ == 12 {
			return fmt.Errorf("%w: symlink-like entry", ErrInvalidImage)
		}
		ce, err := r.udfEntry(part, child.block, p)
		if err != nil {
			return err
		}
		*out = append(*out, ce)
		if len(*out) > maxEntries {
			return fmt.Errorf("%w: too many entries", ErrInvalidImage)
		}
		if typ == 4 {
			if err := r.walkUDF(part, child.block, p, depth+1, seen, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseUDFMetadata(block []byte) (udfMetadata, error) {
	var metadata udfMetadata
	switch binary.LittleEndian.Uint16(block[:2]) {
	case 261:
		metadata = udfMetadata{binary.LittleEndian.Uint64(block[56:64]), binary.LittleEndian.Uint32(block[168:172]), binary.LittleEndian.Uint32(block[172:176]), 176, block[27], binary.LittleEndian.Uint16(block[34:36]) & 7}
	case 266:
		metadata = udfMetadata{binary.LittleEndian.Uint64(block[64:72]), binary.LittleEndian.Uint32(block[208:212]), binary.LittleEndian.Uint32(block[212:216]), 216, block[27], binary.LittleEndian.Uint16(block[34:36]) & 7}
	default:
		return udfMetadata{}, fmt.Errorf("%w: unsupported UDF file entry", ErrInvalidImage)
	}
	if uint64(metadata.start)+uint64(metadata.extended)+uint64(metadata.allocation) > uint64(len(block)) {
		return udfMetadata{}, fmt.Errorf("%w: allocation descriptors", ErrInvalidImage)
	}
	return metadata, nil
}

func (m udfMetadata) descriptors(block []byte) []byte {
	start := m.start + int(m.extended)
	return block[start : start+int(m.allocation)]
}

func parseUDFChild(data []byte, position int) (udfChild, int, bool, error) {
	if binary.LittleEndian.Uint16(data[position:position+2]) != 257 {
		return udfChild{}, 0, false, fmt.Errorf("%w: malformed file identifier", ErrInvalidImage)
	}
	nameLength := int(data[position+19])
	implementationLength := int(binary.LittleEndian.Uint16(data[position+36 : position+38]))
	length := (38 + implementationLength + nameLength + 3) &^ 3
	if length <= 0 || position+length > len(data) {
		return udfChild{}, 0, false, fmt.Errorf("%w: truncated file identifier", ErrInvalidImage)
	}
	if data[position+18]&0x0c != 0 {
		return udfChild{}, position + length, true, nil
	}
	name, err := decodeOSTA(data[position+38+implementationLength : position+38+implementationLength+nameLength])
	child := udfChild{binary.LittleEndian.Uint32(data[position+24 : position+28]), name}
	return child, position + length, false, err
}
func (r *Reader) udfEntry(part, block uint32, p string) (Entry, error) {
	b, err := r.udfBlock(part, block)
	if err != nil {
		return Entry{}, err
	}
	metadata, err := parseUDFMetadata(b)
	if err != nil {
		return Entry{}, err
	}
	ex, err := r.udfExtents(part, metadata.descriptors(b), metadata.allocationType)
	if err != nil {
		return Entry{}, err
	}
	typ := File
	if metadata.typeCode == 4 {
		typ = Directory
	}
	return Entry{Path: p, Type: typ, Extents: ex, Size: metadata.size, DestinationFATPath: fatPath(p)}, nil
}
func (r *Reader) udfExtents(part uint32, b []byte, t uint16) ([]Extent, error) {
	var step, posoff int
	switch t {
	case 0:
		step, posoff = 8, 4
	case 1:
		step, posoff = 16, 4
	case 3:
		return nil, fmt.Errorf("%w: embedded UDF data unsupported", ErrInvalidImage)
	default:
		return nil, fmt.Errorf("%w: unsupported UDF allocation", ErrInvalidImage)
	}
	var out []Extent
	for len(b) >= step {
		raw := binary.LittleEndian.Uint32(b[:4])
		length := uint64(raw & 0x3fffffff)
		block := binary.LittleEndian.Uint32(b[posoff : posoff+4])
		offBlocks := uint64(part) + uint64(block)
		if offBlocks > ^uint64(0)/uint64(sectorSize) {
			return nil, fmt.Errorf("%w: extent overflow", ErrInvalidImage)
		}
		out = append(out, Extent{offBlocks * uint64(sectorSize), length})
		b = b[step:]
	}
	return out, nil
}
func (r *Reader) readExtents(es []Extent, size uint64) ([]byte, error) {
	if size > 64<<20 {
		return nil, fmt.Errorf("%w: directory too large", ErrInvalidImage)
	}
	out := make([]byte, 0, size)
	for _, e := range es {
		n := e.Length
		if n > size-uint64(len(out)) {
			n = size - uint64(len(out))
		}
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
		return nil, fmt.Errorf("%w: short directory", ErrInvalidImage)
	}
	return out, nil
}
func decodeOSTA(b []byte) (string, error) {
	if len(b) == 0 {
		return "", fmt.Errorf("%w: empty UDF name", ErrInvalidImage)
	}
	switch b[0] {
	case 8:
		return string(b[1:]), nil
	case 16:
		u := make([]uint16, 0, (len(b)-1)/2)
		b = b[1:]
		for len(b) >= 2 {
			u = append(u, binary.BigEndian.Uint16(b))
			b = b[2:]
		}
		return string(utf16.Decode(u)), nil
	default:
		return "", fmt.Errorf("%w: UDF name encoding", ErrInvalidImage)
	}
}

func (w isoWalker) walk(rec []byte, prefix string, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("%w: directory depth", ErrInvalidImage)
	}
	if len(rec) < 34 {
		return fmt.Errorf("%w: directory record", ErrInvalidImage)
	}
	ext, size := binary.LittleEndian.Uint32(rec[2:6]), binary.LittleEndian.Uint32(rec[10:14])
	if w.seen[ext] {
		return fmt.Errorf("%w: directory cycle", ErrInvalidImage)
	}
	w.seen[ext] = true
	defer delete(w.seen, ext)
	b, err := w.reader.bytes(uint64(ext)*uint64(sectorSize), uint64(size))
	if err != nil {
		return err
	}
	pending := map[string]int{}
	for pos := 0; pos < len(b); {
		n := int(b[pos])
		if n == 0 {
			pos = ((pos / int(sectorSize)) + 1) * int(sectorSize)
			continue
		}
		if n < 34 || pos+n > len(b) {
			return fmt.Errorf("%w: malformed directory", ErrInvalidImage)
		}
		x := b[pos : pos+n]
		pos += n
		nl := int(x[32])
		if 33+nl > len(x) {
			return fmt.Errorf("%w: malformed name", ErrInvalidImage)
		}
		nb := x[33 : 33+nl]
		if nl == 1 && (nb[0] == 0 || nb[0] == 1) {
			continue
		}
		name := decodeName(nb, w.joliet)
		name = strings.TrimSuffix(name, ";1")
		p, err := normalize(prefix, name)
		if err != nil {
			return err
		}
		dir := x[25]&2 != 0
		e := Entry{Path: p, Type: File, Size: uint64(binary.LittleEndian.Uint32(x[10:14])), DestinationFATPath: fatPath(p)}
		e.Extents = []Extent{{Offset: uint64(binary.LittleEndian.Uint32(x[2:6])) * uint64(sectorSize), Length: e.Size}}
		if dir {
			e.Type = Directory
		}
		if idx, ok := pending[p]; ok && !dir {
			(*w.out)[idx].Extents = append((*w.out)[idx].Extents, e.Extents...)
			if (*w.out)[idx].Size > ^uint64(0)-e.Size {
				return fmt.Errorf("%w: multi-extent overflow", ErrInvalidImage)
			}
			(*w.out)[idx].Size += e.Size
			if x[25]&0x80 == 0 {
				delete(pending, p)
			}
			continue
		}
		*w.out = append(*w.out, e)
		if !dir && x[25]&0x80 != 0 {
			pending[p] = len(*w.out) - 1
		}
		if len(*w.out) > maxEntries {
			return fmt.Errorf("%w: too many entries", ErrInvalidImage)
		}
		if dir {
			if err := w.walk(x, p, depth+1); err != nil {
				return err
			}
		}
	}
	if len(pending) != 0 {
		return fmt.Errorf("%w: unterminated multi-extent file", ErrInvalidImage)
	}
	return nil
}

func decodeName(b []byte, j bool) string {
	if !j {
		return string(b)
	}
	u := make([]uint16, 0, len(b)/2)
	for len(b) >= 2 {
		u = append(u, binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	return string(utf16.Decode(u))
}
func normalize(parent, name string) (string, error) {
	if strings.ContainsRune(name, 0) || strings.Contains(name, "/") || strings.Contains(name, "\\") || name == ".." || name == "." || path.IsAbs(name) {
		return "", fmt.Errorf("%w: unsafe path", ErrInvalidImage)
	}
	p := name
	if parent != "" {
		p = parent + "/" + name
	}
	p = path.Clean(p)
	if p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: traversal", ErrInvalidImage)
	}
	return p, nil
}
func fatPath(p string) string { return strings.ReplaceAll(p, "/", "\\") }

func (r *Reader) validate(es []Entry) error {
	paths := map[string]bool{}
	fold := map[string]string{}
	var spans []usedExtent
	for _, e := range es {
		entrySpans, err := r.validateEntry(e, paths, fold)
		if err != nil {
			return err
		}
		spans = append(spans, entrySpans...)
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end && spans[i].path != spans[i-1].path {
			return fmt.Errorf("%w: extent collision", ErrInvalidImage)
		}
	}
	return nil
}

func (r *Reader) validateEntry(entry Entry, paths map[string]bool, folded map[string]string) ([]usedExtent, error) {
	if entry.Path == "" || paths[entry.Path] {
		return nil, fmt.Errorf("%w: duplicate path", ErrInvalidImage)
	}
	paths[entry.Path] = true
	key := norm.NFC.String(cases.Fold().String(entry.Path))
	if old, exists := folded[key]; exists && old != entry.Path {
		return nil, fmt.Errorf("%w: case or Unicode collision", ErrInvalidImage)
	}
	folded[key] = entry.Path
	if entry.Type != File && entry.Type != Directory {
		return nil, fmt.Errorf("%w: symlink-like entry", ErrInvalidImage)
	}
	return r.validateEntryExtents(entry)
}

func (r *Reader) validateEntryExtents(entry Entry) ([]usedExtent, error) {
	var total uint64
	var spans []usedExtent
	for _, extent := range entry.Extents {
		if extent.Offset > r.size || extent.Length > r.size-extent.Offset || total > extent.Length+total {
			return nil, fmt.Errorf("%w: extent overflow", ErrInvalidImage)
		}
		total += extent.Length
		if extent.Length > 0 {
			spans = append(spans, usedExtent{extent.Offset, extent.Offset + extent.Length, entry.Path})
		}
	}
	if entry.Type == File && total < entry.Size {
		return nil, fmt.Errorf("%w: short extent", ErrInvalidImage)
	}
	return spans, nil
}
