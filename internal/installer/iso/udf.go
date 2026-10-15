package iso

// This file implements UDF (ECMA-167) parsing, the fallback for images that
// carry no usable ISO9660 volume descriptor.

import "encoding/binary"

// udfPartition is the sector where the one supported physical partition
// starts; file entries and allocation descriptors address blocks relative to
// it.
type udfPartition uint32

// logicalBlock is a UDF block number relative to the partition start.
type logicalBlock uint32

// at resolves a partition-relative block to an absolute sector. Both halves
// are 32-bit, so the sum cannot overflow an lba.
func (p udfPartition) at(b logicalBlock) lba { return lba(p) + lba(b) }

// extent resolves an allocation descriptor to the byte range it covers.
func (p udfPartition) extent(d allocDescriptor) (Extent, error) {
	start := p.at(d.block())
	if !start.addressable() {
		return Extent{}, invalid("extent overflow")
	}
	return Extent{Offset: start.offset(), Length: d.length()}, nil
}

// udfLayout is what the volume descriptor sequence contributes: the physical
// partition start and the file set descriptor location within it.
type udfLayout struct {
	partitionStart udfPartition
	fsdBlock       logicalBlock
}

// readUDF implements the read-only ECMA-167 subset used by Windows mastering
// media (UDF 1.02/2.x, one physical partition, short/long allocation
// descriptors). Unsupported allocation forms fail closed.
func (r *Reader) readUDF() ([]Entry, error) {
	seq, err := r.readUDFDescriptorSequence()
	if err != nil {
		return nil, err
	}
	layout, err := seq.layout()
	if err != nil {
		return nil, err
	}
	root, err := r.udfRootBlock(layout)
	if err != nil {
		return nil, err
	}
	w := &udfWalker{r: r, part: layout.partitionStart, seen: map[logicalBlock]bool{}}
	if err := w.walk(root, treePos{}); err != nil {
		return nil, err
	}
	return w.out, nil
}

// descriptorSequence is the main volume descriptor sequence the anchor
// points to.
type descriptorSequence []byte

func (r *Reader) readUDFDescriptorSequence() (descriptorSequence, error) {
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
	b, err := r.bytes(Extent{Offset: lba(seqBlock).offset(), Length: uint64(seqLen)})
	return descriptorSequence(b), err
}

func (seq descriptorSequence) layout() (udfLayout, error) {
	var layout udfLayout
	havePartition, haveLVD := false, false
	for off := 0; off+512 <= len(seq); off += int(sectorSize) {
		d := seq[off:]
		switch binary.LittleEndian.Uint16(d[:2]) {
		case 5:
			layout.partitionStart = udfPartition(binary.LittleEndian.Uint32(d[188:192]))
			havePartition = true
		case 6:
			if binary.LittleEndian.Uint32(d[212:216]) != uint32(sectorSize) {
				return udfLayout{}, invalid("unsupported UDF block size")
			}
			layout.fsdBlock = logicalBlock(binary.LittleEndian.Uint32(d[252:256]))
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

func (r *Reader) udfRootBlock(layout udfLayout) (logicalBlock, error) {
	fsd, err := r.sector(layout.partitionStart.at(layout.fsdBlock))
	if err != nil {
		return 0, err
	}
	if binary.LittleEndian.Uint16(fsd[:2]) != 256 {
		return 0, invalid("missing file set descriptor")
	}
	return logicalBlock(binary.LittleEndian.Uint32(fsd[404:408])), nil
}

// udfFileType is the ICB tag file type of a file entry.
type udfFileType byte

const (
	udfFileTypeDirectory udfFileType = 4
	udfFileTypeSymlink   udfFileType = 12
)

// allocForm is the allocation descriptor form an ICB tag declares.
type allocForm uint16

const (
	allocShort    allocForm = 0
	allocLong     allocForm = 1
	allocEmbedded allocForm = 3
)

// descriptorSize is the byte length of one allocation descriptor of this form.
func (f allocForm) descriptorSize() (int, error) {
	switch f {
	case allocShort:
		return 8, nil
	case allocLong:
		return 16, nil
	case allocEmbedded:
		return 0, invalid("embedded UDF data unsupported")
	default:
		return 0, invalid("unsupported UDF allocation")
	}
}

// allocDescriptor is a short or long allocation descriptor. Both begin with
// the extent length, whose top two bits hold the extent type, followed by the
// partition-relative block.
type allocDescriptor []byte

func (d allocDescriptor) length() uint64 {
	return uint64(binary.LittleEndian.Uint32(d[:4]) & 0x3fffffff)
}

func (d allocDescriptor) block() logicalBlock {
	return logicalBlock(binary.LittleEndian.Uint32(d[4:8]))
}

// udfFileEntry is the parsed common subset of a File Entry (tag 261) and an
// Extended File Entry (tag 266).
type udfFileEntry struct {
	fileType  udfFileType
	infoLen   uint64
	allocType allocForm
	allocs    []byte
}

func (e udfFileEntry) isDir() bool {
	return e.fileType == udfFileTypeDirectory
}

// extents resolves the entry's allocation descriptors within partition p.
func (e udfFileEntry) extents(p udfPartition) ([]Extent, error) {
	step, err := e.allocType.descriptorSize()
	if err != nil {
		return nil, err
	}
	var out []Extent
	for b := e.allocs; len(b) >= step; b = b[step:] {
		x, err := p.extent(allocDescriptor(b[:step]))
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}

func (e udfFileEntry) data(p udfPartition) (extentData, error) {
	exts, err := e.extents(p)
	return extentData{extents: exts, size: e.infoLen}, err
}

// fileEntryBlock is the raw block holding a File Entry or Extended File Entry.
type fileEntryBlock []byte

func (b fileEntryBlock) parse() (udfFileEntry, error) {
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
	if udfFileType(b[27]) == udfFileTypeSymlink {
		return udfFileEntry{}, invalid("symlink-like entry")
	}
	if uint64(start)+uint64(ea)+uint64(ad) > uint64(len(b)) {
		return udfFileEntry{}, invalid("allocation descriptors")
	}
	allocStart := start + int(ea)
	return udfFileEntry{
		fileType:  udfFileType(b[27]),
		infoLen:   infoLen,
		allocType: allocForm(binary.LittleEndian.Uint16(b[34:36]) & 7),
		allocs:    b[allocStart : allocStart+int(ad)],
	}, nil
}

// ostaName is a UDF dstring in OSTA compressed Unicode: a compression ID byte
// (8 or 16 bits per character) followed by the characters.
type ostaName []byte

func (b ostaName) decode() (entryName, error) {
	if len(b) == 0 {
		return "", invalid("empty UDF name")
	}
	switch b[0] {
	case 8:
		return entryName(b[1:]), nil
	case 16:
		return entryName(ucs2BE(b[1:]).String()), nil
	default:
		return "", invalid("UDF name encoding")
	}
}

type udfWalker struct {
	r    *Reader
	part udfPartition
	seen map[logicalBlock]bool
	out  []Entry
}

func (w *udfWalker) fileEntry(block logicalBlock) (udfFileEntry, error) {
	b, err := w.r.sector(w.part.at(block))
	if err != nil {
		return udfFileEntry{}, err
	}
	return fileEntryBlock(b).parse()
}

func (w *udfWalker) walk(block logicalBlock, at treePos) error {
	if err := w.enter(block, at); err != nil {
		return err
	}
	defer w.leave(block)
	fids, err := w.directoryIdentifiers(block)
	if err != nil {
		return err
	}
	return w.addChildren(fids, at)
}

func (w *udfWalker) enter(block logicalBlock, at treePos) error {
	if at.tooDeep() {
		return invalid("directory depth")
	}
	if w.seen[block] {
		return invalid("directory cycle")
	}
	w.seen[block] = true
	return nil
}

func (w *udfWalker) leave(block logicalBlock) { delete(w.seen, block) }

func (w *udfWalker) directoryIdentifiers(block logicalBlock) ([]udfFID, error) {
	fe, err := w.fileEntry(block)
	if err != nil {
		return nil, err
	}
	d, err := fe.data(w.part)
	if err != nil {
		return nil, err
	}
	if !fe.isDir() {
		return nil, invalid("root is not directory")
	}
	data, err := w.r.readData(d)
	if err != nil {
		return nil, err
	}
	return fidStream(data).identifiers()
}

func (w *udfWalker) addChildren(fids []udfFID, at treePos) error {
	for _, fid := range fids {
		if err := w.addChild(fid, at); err != nil {
			return err
		}
	}
	return nil
}

// udfFID is a File Identifier Descriptor (tag 257) reduced to what the walk
// needs.
type udfFID struct {
	name  ostaName
	child logicalBlock
}

// fidStream is a directory's data: a run of File Identifier Descriptors.
type fidStream []byte

// identifiers splits a directory's data into identifiers, dropping deleted
// entries and parent links (flag bits 2 and 3).
func (data fidStream) identifiers() ([]udfFID, error) {
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
		fid := udfFID{name: ostaName(data[pos+38+implLen : pos+38+implLen+nameLen]), child: logicalBlock(binary.LittleEndian.Uint32(data[pos+24 : pos+28]))}
		pos += n
		if flags&0x0c != 0 {
			continue
		}
		out = append(out, fid)
	}
	return out, nil
}

func (w *udfWalker) addChild(fid udfFID, at treePos) error {
	name, err := fid.name.decode()
	if err != nil {
		return err
	}
	p, err := at.path.child(name)
	if err != nil {
		return err
	}
	ce, err := w.entry(fid.child, p)
	if err != nil {
		return err
	}
	w.out = append(w.out, ce)
	if len(w.out) > maxEntries {
		return invalid("too many entries")
	}
	if ce.Type == Directory {
		return w.walk(fid.child, at.descend(p))
	}
	return nil
}

func (w *udfWalker) entry(block logicalBlock, p entryPath) (Entry, error) {
	fe, err := w.fileEntry(block)
	if err != nil {
		return Entry{}, err
	}
	d, err := fe.data(w.part)
	if err != nil {
		return Entry{}, err
	}
	typ := File
	if fe.isDir() {
		typ = Directory
	}
	return Entry{Path: string(p), Type: typ, Extents: d.extents, Size: d.size, DestinationFATPath: p.fatPath()}, nil
}
