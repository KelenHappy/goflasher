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
	if err := validateSource(source, size, retained); err != nil {
		return nil, err
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

func validateSource(source io.ReaderAt, size int64, retained io.Closer) error {
	if source == nil {
		return invalid("invalid source")
	}
	if retained == nil {
		return invalid("invalid source")
	}
	if size < 0 {
		return invalid("invalid source")
	}
	return nil
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
	if !r.validRead(off, length) {
		return nil, invalid("extent out of bounds")
	}
	b := make([]byte, length)
	if _, err := r.source.ReadAt(b, int64(off)); err != nil {
		return nil, invalid("truncated extent: %v", err)
	}
	return b, nil
}

func (r *Reader) validRead(off, length uint64) bool {
	if length > maxRead || off > r.size {
		return false
	}
	return length <= r.size-off
}

func (r *Reader) sector(n uint64) ([]byte, error) {
	return r.bytes(n*uint64(sectorSize), uint64(sectorSize))
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
	if name == "." || name == ".." {
		return true
	}
	return path.IsAbs(name) || strings.ContainsAny(name, "\x00/\\")
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
