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
		if err := r.walkISO(b[156:190], "", joliet, 0, map[uint32]bool{}, &entries); err != nil {
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
	if err := r.walkUDF(partitionStart, root, "", 0, map[uint32]bool{}, &out, true); err != nil {
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
func (r *Reader) walkUDF(part, block uint32, prefix string, depth int, seen map[uint32]bool, out *[]Entry, root bool) error {
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
	tag := binary.LittleEndian.Uint16(fe[:2])
	var info uint64
	var ea, ad uint32
	var start int
	switch tag {
	case 261:
		info = binary.LittleEndian.Uint64(fe[56:64])
		ea = binary.LittleEndian.Uint32(fe[168:172])
		ad = binary.LittleEndian.Uint32(fe[172:176])
		start = 176
	case 266:
		info = binary.LittleEndian.Uint64(fe[64:72])
		ea = binary.LittleEndian.Uint32(fe[208:212])
		ad = binary.LittleEndian.Uint32(fe[212:216])
		start = 216
	default:
		return fmt.Errorf("%w: unsupported UDF file entry", ErrInvalidImage)
	}
	if fe[27] == 12 {
		return fmt.Errorf("%w: symlink-like entry", ErrInvalidImage)
	}
	allocType := binary.LittleEndian.Uint16(fe[34:36]) & 7
	if uint64(start)+uint64(ea)+uint64(ad) > uint64(len(fe)) {
		return fmt.Errorf("%w: allocation descriptors", ErrInvalidImage)
	}
	exts, err := r.udfExtents(part, fe[start+int(ea):start+int(ea)+int(ad)], allocType)
	if err != nil {
		return err
	}
	if fe[27] != 4 {
		return fmt.Errorf("%w: root is not directory", ErrInvalidImage)
	}
	data, err := r.readExtents(exts, info)
	if err != nil {
		return err
	}
	for pos := 0; pos+38 <= len(data); {
		if binary.LittleEndian.Uint16(data[pos:pos+2]) != 257 {
			return fmt.Errorf("%w: malformed file identifier", ErrInvalidImage)
		}
		flags := data[pos+18]
		nl := int(data[pos+19])
		iu := int(binary.LittleEndian.Uint16(data[pos+36 : pos+38]))
		n := 38 + iu + nl
		n = (n + 3) &^ 3
		if n <= 0 || pos+n > len(data) {
			return fmt.Errorf("%w: truncated file identifier", ErrInvalidImage)
		}
		nameb := data[pos+38+iu : pos+38+iu+nl]
		child := binary.LittleEndian.Uint32(data[pos+24 : pos+28])
		pos += n
		if flags&0x0c != 0 {
			continue
		}
		name, err := decodeOSTA(nameb)
		if err != nil {
			return err
		}
		p, err := normalize(prefix, name)
		if err != nil {
			return err
		}
		childFE, err := r.udfBlock(part, child)
		if err != nil {
			return err
		}
		typ := childFE[27]
		if typ == 12 {
			return fmt.Errorf("%w: symlink-like entry", ErrInvalidImage)
		}
		ce, err := r.udfEntry(part, child, p)
		if err != nil {
			return err
		}
		*out = append(*out, ce)
		if len(*out) > maxEntries {
			return fmt.Errorf("%w: too many entries", ErrInvalidImage)
		}
		if typ == 4 {
			if err := r.walkUDF(part, child, p, depth+1, seen, out, false); err != nil {
				return err
			}
		}
	}
	_ = root
	return nil
}
func (r *Reader) udfEntry(part, block uint32, p string) (Entry, error) {
	b, err := r.udfBlock(part, block)
	if err != nil {
		return Entry{}, err
	}
	tag := binary.LittleEndian.Uint16(b[:2])
	var size uint64
	var ea, ad uint32
	var st int
	if tag == 261 {
		size = binary.LittleEndian.Uint64(b[56:64])
		ea = binary.LittleEndian.Uint32(b[168:172])
		ad = binary.LittleEndian.Uint32(b[172:176])
		st = 176
	} else if tag == 266 {
		size = binary.LittleEndian.Uint64(b[64:72])
		ea = binary.LittleEndian.Uint32(b[208:212])
		ad = binary.LittleEndian.Uint32(b[212:216])
		st = 216
	} else {
		return Entry{}, fmt.Errorf("%w: file entry tag", ErrInvalidImage)
	}
	if uint64(st)+uint64(ea)+uint64(ad) > uint64(len(b)) {
		return Entry{}, fmt.Errorf("%w: allocation descriptors", ErrInvalidImage)
	}
	ex, err := r.udfExtents(part, b[st+int(ea):st+int(ea)+int(ad)], binary.LittleEndian.Uint16(b[34:36])&7)
	if err != nil {
		return Entry{}, err
	}
	typ := File
	if b[27] == 4 {
		typ = Directory
	}
	return Entry{Path: p, Type: typ, Extents: ex, Size: size, DestinationFATPath: fatPath(p)}, nil
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

func (r *Reader) walkISO(rec []byte, prefix string, joliet bool, depth int, seen map[uint32]bool, out *[]Entry) error {
	if depth > maxDepth {
		return fmt.Errorf("%w: directory depth", ErrInvalidImage)
	}
	if len(rec) < 34 {
		return fmt.Errorf("%w: directory record", ErrInvalidImage)
	}
	ext, size := binary.LittleEndian.Uint32(rec[2:6]), binary.LittleEndian.Uint32(rec[10:14])
	if seen[ext] {
		return fmt.Errorf("%w: directory cycle", ErrInvalidImage)
	}
	seen[ext] = true
	defer delete(seen, ext)
	b, err := r.bytes(uint64(ext)*uint64(sectorSize), uint64(size))
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
		name := decodeName(nb, joliet)
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
			(*out)[idx].Extents = append((*out)[idx].Extents, e.Extents...)
			if (*out)[idx].Size > ^uint64(0)-e.Size {
				return fmt.Errorf("%w: multi-extent overflow", ErrInvalidImage)
			}
			(*out)[idx].Size += e.Size
			if x[25]&0x80 == 0 {
				delete(pending, p)
			}
			continue
		}
		*out = append(*out, e)
		if !dir && x[25]&0x80 != 0 {
			pending[p] = len(*out) - 1
		}
		if len(*out) > maxEntries {
			return fmt.Errorf("%w: too many entries", ErrInvalidImage)
		}
		if dir {
			if err := r.walkISO(x, p, joliet, depth+1, seen, out); err != nil {
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
	type used struct {
		a, b uint64
		p    string
	}
	var spans []used
	for _, e := range es {
		if e.Path == "" || paths[e.Path] {
			return fmt.Errorf("%w: duplicate path", ErrInvalidImage)
		}
		paths[e.Path] = true
		k := norm.NFC.String(cases.Fold().String(e.Path))
		if old, ok := fold[k]; ok && old != e.Path {
			return fmt.Errorf("%w: case or Unicode collision", ErrInvalidImage)
		}
		fold[k] = e.Path
		if e.Type != File && e.Type != Directory {
			return fmt.Errorf("%w: symlink-like entry", ErrInvalidImage)
		}
		var total uint64
		for _, x := range e.Extents {
			if x.Offset > r.size || x.Length > r.size-x.Offset || total > x.Length+total {
				return fmt.Errorf("%w: extent overflow", ErrInvalidImage)
			}
			total += x.Length
			if x.Length > 0 {
				spans = append(spans, used{x.Offset, x.Offset + x.Length, e.Path})
			}
		}
		if e.Type == File && total < e.Size {
			return fmt.Errorf("%w: short extent", ErrInvalidImage)
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].a < spans[j].a })
	for i := 1; i < len(spans); i++ {
		if spans[i].a < spans[i-1].b && spans[i].p != spans[i-1].p {
			return fmt.Errorf("%w: extent collision", ErrInvalidImage)
		}
	}
	return nil
}
