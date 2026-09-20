package iso

// This file implements UDF (ECMA-167) parsing, the fallback for images that
// carry no usable ISO9660 volume descriptor.

import "encoding/binary"

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

func (e udfFileEntry) isDir() bool {
	return e.fileType == udfFileTypeDirectory
}

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
	if err := w.enter(block, depth); err != nil {
		return err
	}
	defer w.leave(block)
	fids, err := w.directoryIdentifiers(block)
	if err != nil {
		return err
	}
	return w.addChildren(fids, prefix, depth)
}

func (w *udfWalker) enter(block uint32, depth int) error {
	if depth > maxDepth {
		return invalid("directory depth")
	}
	if w.seen[block] {
		return invalid("directory cycle")
	}
	w.seen[block] = true
	return nil
}

func (w *udfWalker) leave(block uint32) { delete(w.seen, block) }

func (w *udfWalker) directoryIdentifiers(block uint32) ([]udfFID, error) {
	fe, err := w.r.udfFileEntry(w.part, block)
	if err != nil {
		return nil, err
	}
	exts, err := udfExtents(w.part, fe.allocs, fe.allocType)
	if err != nil {
		return nil, err
	}
	if !fe.isDir() {
		return nil, invalid("root is not directory")
	}
	data, err := w.r.readExtents(exts, fe.infoLen)
	if err != nil {
		return nil, err
	}
	return parseUDFFileIdentifiers(data)
}

func (w *udfWalker) addChildren(fids []udfFID, prefix string, depth int) error {
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
	pos := 0
	for pos+38 <= len(data) {
		if binary.LittleEndian.Uint16(data[pos:pos+2]) != 257 {
			return nil, invalid("malformed file identifier")
		}
		flags := data[pos+18]
		nameLen := int(data[pos+19])
		implLen := int(binary.LittleEndian.Uint16(data[pos+36 : pos+38]))
		n := (38 + implLen + nameLen + 3) / 4 * 4
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
