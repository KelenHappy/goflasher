package iso

// This file implements the ISO9660 volume descriptor scan and directory
// record walk, including the Joliet extension Windows media relies on.

import (
	"encoding/binary"
	"strings"
)

// readISO9660 scans the volume descriptor set. Joliet is preferred because it
// preserves the names Windows media actually uses.
func (r *Reader) readISO9660() ([]Entry, bool, error) {
	var all []Entry
	found := false
	for sec := lba(16); sec < 256; sec++ {
		entries, kind, err := r.readISODescriptor(sec)
		if err != nil {
			return nil, found, err
		}
		if kind == descriptorTerminator {
			break
		}
		if !kind.usable() {
			continue
		}
		found = true
		if kind == descriptorJoliet || len(all) == 0 {
			all = entries
		}
	}
	return all, found, nil
}

// readISODescriptor classifies the descriptor at sec and, when it carries a
// directory tree, walks it.
func (r *Reader) readISODescriptor(sec lba) ([]Entry, descriptorKind, error) {
	b, err := r.sector(sec)
	if err != nil {
		return nil, descriptorOther, err
	}
	d := volumeDescriptor(b)
	kind := d.kind()
	if !kind.usable() {
		return nil, kind, nil
	}
	entries, err := r.walkVolumeDescriptor(d, kind.names())
	return entries, kind, err
}

// descriptorKind is the role a volume descriptor plays in the set.
type descriptorKind int

const (
	descriptorOther descriptorKind = iota
	descriptorPrimary
	descriptorJoliet
	descriptorTerminator
)

func (k descriptorKind) usable() bool { return k == descriptorPrimary || k == descriptorJoliet }

func (k descriptorKind) names() nameEncoding { return nameEncoding(k == descriptorJoliet) }

// volumeDescriptor is one 2048-byte sector of the volume descriptor set.
type volumeDescriptor []byte

func (d volumeDescriptor) kind() descriptorKind {
	if string(d[1:6]) != "CD001" {
		return descriptorOther
	}
	switch {
	case d[0] == 255:
		return descriptorTerminator
	case d[0] == 1:
		return descriptorPrimary
	case d[0] == 2 && d.jolietEscapeSequence():
		return descriptorJoliet
	default:
		return descriptorOther
	}
}

// jolietEscapeSequence reports the UCS-2 escape sequence at offset 88 that
// distinguishes a Joliet supplementary descriptor from any other one.
func (d volumeDescriptor) jolietEscapeSequence() bool {
	return d[88] == 0x25 && d[89] == 0x2f
}

func (d volumeDescriptor) rootRecord() dirRecord { return dirRecord(d[156:190]) }

// nameEncoding is how a descriptor's directory records encode identifiers:
// plain d-characters, or UCS-2 big-endian under Joliet.
type nameEncoding bool

const jolietNames nameEncoding = true

func (e nameEncoding) decode(id isoIdentifier) string {
	if e == jolietNames {
		return ucs2BE(id).String()
	}
	return string(id)
}

// isoIdentifier is the raw file identifier bytes of a directory record.
type isoIdentifier []byte

// dot reports the reserved single-byte identifiers: 0x00 is "." and 0x01 is
// "..".
func (id isoIdentifier) dot() bool {
	if len(id) != 1 {
		return false
	}
	return id[0] == 0 || id[0] == 1
}

// dirRecord is one ISO9660 directory record.
type dirRecord []byte

func (x dirRecord) extent() lba { return lba(binary.LittleEndian.Uint32(x[2:6])) }

func (x dirRecord) dataLength() uint64 { return uint64(binary.LittleEndian.Uint32(x[10:14])) }

func (x dirRecord) data() Extent { return Extent{Offset: x.extent().offset(), Length: x.dataLength()} }

func (x dirRecord) isDir() bool { return x[25]&2 != 0 }

// continues reports that further records of the same multi-extent file follow.
func (x dirRecord) continues() bool { return x[25]&0x80 != 0 }

// name decodes the record's file identifier. skip reports the "." and ".."
// entries.
func (x dirRecord) name(names nameEncoding) (name entryName, skip bool, err error) {
	nl := int(x[32])
	if 33+nl > len(x) {
		return "", false, invalid("malformed name")
	}
	id := isoIdentifier(x[33 : 33+nl])
	if id.dot() {
		return "", true, nil
	}
	return entryName(names.decode(id)).withoutVersion(), false, nil
}

func (x dirRecord) entry(p entryPath) Entry {
	e := Entry{Path: string(p), Type: File, Size: x.dataLength(), Extents: []Extent{x.data()}, DestinationFATPath: p.fatPath()}
	if x.isDir() {
		e.Type = Directory
	}
	return e
}

// withoutVersion strips the ";N" version suffix and, for extensionless
// ISO9660 names such as "BOOTMGR.;1", the trailing separator dot.
func (n entryName) withoutVersion() entryName {
	s := string(n)
	if i := strings.LastIndexByte(s, ';'); i > 0 {
		s = s[:i]
	}
	if trimmed := strings.TrimSuffix(s, "."); trimmed != "" {
		s = trimmed
	}
	return entryName(s)
}

// dirData is the concatenated records of one directory.
type dirData []byte

// records splits directory data into records. A zero length byte marks
// sector padding; the next record begins at the following sector.
func (b dirData) records() ([]dirRecord, error) {
	var out []dirRecord
	pos := 0
	for pos < len(b) {
		n := int(b[pos])
		if n == 0 {
			pos = ((pos / int(sectorSize)) + 1) * int(sectorSize)
			continue
		}
		if n < 34 || pos+n > len(b) {
			return nil, invalid("malformed directory")
		}
		out = append(out, dirRecord(b[pos:pos+n]))
		pos += n
	}
	return out, nil
}

func (r *Reader) walkVolumeDescriptor(d volumeDescriptor, names nameEncoding) ([]Entry, error) {
	w := &isoWalker{r: r, names: names, seen: map[lba]bool{}}
	if err := w.walk(d.rootRecord(), treePos{}); err != nil {
		return nil, err
	}
	return w.out, nil
}

type isoWalker struct {
	r     *Reader
	names nameEncoding
	seen  map[lba]bool
	out   []Entry
}

func (w *isoWalker) walk(rec dirRecord, at treePos) error {
	ext, err := w.enter(rec, at)
	if err != nil {
		return err
	}
	defer w.leave(ext)
	records, err := w.directoryRecords(rec)
	if err != nil {
		return err
	}
	return w.addRecords(records, at)
}

func (w *isoWalker) enter(rec dirRecord, at treePos) (lba, error) {
	if at.tooDeep() {
		return 0, invalid("directory depth")
	}
	if len(rec) < 34 {
		return 0, invalid("directory record")
	}
	ext := rec.extent()
	if w.seen[ext] {
		return 0, invalid("directory cycle")
	}
	w.seen[ext] = true
	return ext, nil
}

func (w *isoWalker) leave(ext lba) { delete(w.seen, ext) }

func (w *isoWalker) directoryRecords(rec dirRecord) ([]dirRecord, error) {
	b, err := w.r.bytes(rec.data())
	if err != nil {
		return nil, err
	}
	return dirData(b).records()
}

func (w *isoWalker) addRecords(records []dirRecord, at treePos) error {
	d := &isoDir{w: w, at: at, pending: map[string]int{}}
	for _, x := range records {
		if err := d.addRecord(x); err != nil {
			return err
		}
	}
	if len(d.pending) != 0 {
		return invalid("unterminated multi-extent file")
	}
	return nil
}

// isoDir is the state of one directory's record scan. pending maps each
// multi-extent file still awaiting records to its index in the walker output.
type isoDir struct {
	w       *isoWalker
	at      treePos
	pending map[string]int
}

func (d *isoDir) addRecord(x dirRecord) error {
	name, skip, err := x.name(d.w.names)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}
	p, err := d.at.path.child(name)
	if err != nil {
		return err
	}
	e := x.entry(p)
	if target, ok := d.pendingFile(e); ok {
		return d.mergeExtent(target, e, x)
	}
	return d.addEntry(x, e)
}

// pendingFile returns the entry already collected for e when e continues a
// multi-extent file.
func (d *isoDir) pendingFile(e Entry) (*Entry, bool) {
	if e.Type != File {
		return nil, false
	}
	idx, ok := d.pending[e.Path]
	if !ok {
		return nil, false
	}
	return &d.w.out[idx], true
}

func (d *isoDir) addEntry(record dirRecord, e Entry) error {
	w := d.w
	w.out = append(w.out, e)
	if e.Type == File && record.continues() {
		d.pending[e.Path] = len(w.out) - 1
	}
	if len(w.out) > maxEntries {
		return invalid("too many entries")
	}
	if e.Type == Directory {
		return w.walk(record, d.at.descend(entryPath(e.Path)))
	}
	return nil
}

// mergeExtent appends a continuation record of a multi-extent file to the
// entry already collected for it.
func (d *isoDir) mergeExtent(target *Entry, e Entry, record dirRecord) error {
	if target.Size > ^uint64(0)-e.Size {
		return invalid("multi-extent overflow")
	}
	target.Extents = append(target.Extents, e.Extents...)
	target.Size += e.Size
	if !record.continues() {
		delete(d.pending, e.Path)
	}
	return nil
}
