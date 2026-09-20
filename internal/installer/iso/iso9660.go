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
	for sec := uint64(16); sec < 256; sec++ {
		entries, joliet, usable, stop, err := r.readISODescriptor(sec)
		if err != nil {
			return nil, found, err
		}
		if stop {
			break
		}
		if !usable {
			continue
		}
		found = true
		if joliet || len(all) == 0 {
			all = entries
		}
	}
	return all, found, nil
}

// readISODescriptor returns the descriptor's entries and whether it is Joliet,
// usable, and the set terminator, in that order.
func (r *Reader) readISODescriptor(sector uint64) ([]Entry, bool, bool, bool, error) {
	b, err := r.sector(sector)
	if err != nil {
		return nil, false, false, false, err
	}
	joliet, usable, stop := classifyVolumeDescriptor(b)
	if !usable {
		return nil, joliet, false, stop, nil
	}
	entries, err := r.walkVolumeDescriptor(b, joliet)
	return entries, joliet, true, false, err
}

// classifyVolumeDescriptor reports whether b is Joliet, usable, and the set
// terminator, in that order.
func classifyVolumeDescriptor(b []byte) (bool, bool, bool) {
	if !isVolumeDescriptor(b) {
		return false, false, false
	}
	if b[0] == 255 {
		return false, false, true
	}
	joliet, usable := usableVolumeDescriptor(b)
	return joliet, usable, false
}

func isVolumeDescriptor(b []byte) bool { return string(b[1:6]) == "CD001" }

// usableVolumeDescriptor reports whether b is a primary descriptor or a
// Joliet supplementary descriptor (UCS-2 escape sequence), and which. The
// results are joliet then usable.
func usableVolumeDescriptor(b []byte) (bool, bool) {
	if b[0] == 1 {
		return false, true
	}
	if b[0] == 2 && jolietEscapeSequence(b) {
		return true, true
	}
	return false, false
}

// jolietEscapeSequence reports the UCS-2 escape sequence at offset 88 that
// distinguishes a Joliet supplementary descriptor from any other one.
func jolietEscapeSequence(b []byte) bool {
	return b[88] == 0x25 && b[89] == 0x2f
}

func (r *Reader) walkVolumeDescriptor(b []byte, joliet bool) ([]Entry, error) {
	w := &isoWalker{r: r, joliet: joliet, seen: map[uint32]bool{}}
	if err := w.walk(b[156:190], "", 0); err != nil {
		return nil, err
	}
	return w.out, nil
}

type isoWalker struct {
	r      *Reader
	joliet bool
	seen   map[uint32]bool
	out    []Entry
}

func (w *isoWalker) walk(rec []byte, prefix string, depth int) error {
	ext, err := w.enter(rec, depth)
	if err != nil {
		return err
	}
	defer w.leave(ext)
	records, err := w.directoryRecords(rec)
	if err != nil {
		return err
	}
	return w.addRecords(records, prefix, depth)
}

func (w *isoWalker) enter(rec []byte, depth int) (uint32, error) {
	if depth > maxDepth {
		return 0, invalid("directory depth")
	}
	if len(rec) < 34 {
		return 0, invalid("directory record")
	}
	ext := binary.LittleEndian.Uint32(rec[2:6])
	if w.seen[ext] {
		return 0, invalid("directory cycle")
	}
	w.seen[ext] = true
	return ext, nil
}

func (w *isoWalker) leave(ext uint32) { delete(w.seen, ext) }

func (w *isoWalker) directoryRecords(rec []byte) ([][]byte, error) {
	ext := binary.LittleEndian.Uint32(rec[2:6])
	size := binary.LittleEndian.Uint32(rec[10:14])
	b, err := w.r.bytes(uint64(ext)*uint64(sectorSize), uint64(size))
	if err != nil {
		return nil, err
	}
	records, err := isoDirectoryRecords(b)
	if err != nil {
		return nil, err
	}
	return records, nil
}

func (w *isoWalker) addRecords(records [][]byte, prefix string, depth int) error {
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
	if isoDotIdentifier(nb) {
		return "", true, nil
	}
	return strings.TrimSuffix(decodeName(nb, joliet), ";1"), false, nil
}

// isoDotIdentifier reports the reserved single-byte identifiers: 0x00 is "."
// and 0x01 is "..".
func isoDotIdentifier(nb []byte) bool {
	if len(nb) != 1 {
		return false
	}
	return nb[0] == 0 || nb[0] == 1
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
	if err != nil {
		return err
	}
	if skip {
		return nil
	}
	p, err := normalize(prefix, name)
	if err != nil {
		return err
	}
	e := isoEntry(x, p)
	if idx, ok := pendingFile(pending, e); ok {
		return w.mergeExtent(idx, e, isoRecordContinues(x), pending)
	}
	return w.addEntry(x, e, depth, pending)
}

func pendingFile(pending map[string]int, e Entry) (int, bool) {
	if e.Type != File {
		return 0, false
	}
	idx, ok := pending[e.Path]
	return idx, ok
}

func (w *isoWalker) addEntry(record []byte, e Entry, depth int, pending map[string]int) error {
	w.out = append(w.out, e)
	if e.Type == File && isoRecordContinues(record) {
		pending[e.Path] = len(w.out) - 1
	}
	if len(w.out) > maxEntries {
		return invalid("too many entries")
	}
	if e.Type == Directory {
		return w.walk(record, e.Path, depth+1)
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
