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
	size     imageSize
	retained io.Closer
	manifest Manifest
}

// imageSize is the byte length of the source image; every extent the reader
// touches or reports must lie within it.
type imageSize uint64

func (s imageSize) contains(x Extent) bool {
	return x.Offset <= uint64(s) && x.Length <= uint64(s)-x.Offset
}

// lba is a 2048-byte logical sector number; ISO9660 and UDF both address the
// image in these units.
type lba uint64

// addressable reports whether the sector's byte offset fits in a uint64.
func (n lba) addressable() bool { return uint64(n) <= ^uint64(0)/uint64(sectorSize) }

func (n lba) offset() uint64 { return uint64(n) * uint64(sectorSize) }

// extentData is a file body: its extents in order and the bytes they hold.
type extentData struct {
	extents []Extent
	size    uint64
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidImage, fmt.Sprintf(format, args...))
}

// New completely parses and validates the immutable manifest before returning.
// retained pins the caller's source object for both parsing and later extraction.
func New(source io.ReaderAt, size int64, retained io.Closer) (*Reader, error) {
	n, err := sourceSize(source, size, retained)
	if err != nil {
		return nil, err
	}
	r := &Reader{source: source, size: n, retained: retained}
	entries, err := r.parse()
	if err != nil {
		_ = retained.Close()
		return nil, err
	}
	r.manifest.Entries = entries
	return r, nil
}

// sourceSize validates New's arguments and converts the caller's size into
// the imageSize every later bounds check works against.
func sourceSize(source io.ReaderAt, size int64, retained io.Closer) (imageSize, error) {
	if source == nil {
		return 0, invalid("invalid source")
	}
	if retained == nil {
		return 0, invalid("invalid source")
	}
	if size < 0 {
		return 0, invalid("invalid source")
	}
	return imageSize(size), nil
}

// parse prefers UDF when the image carries it: Windows media ship a UDF
// bridge whose ISO9660 tree holds only a README placeholder, so the ISO9660
// view alone would hide the installer content.
func (r *Reader) parse() ([]Entry, error) {
	entries, err := r.readUDF()
	if err != nil {
		var found bool
		entries, found, err = r.readISO9660()
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, invalid("no ISO9660 or UDF filesystem")
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

func (r *Reader) bytes(x Extent) ([]byte, error) {
	if !r.validRead(x) {
		return nil, invalid("extent out of bounds")
	}
	b := make([]byte, x.Length)
	if _, err := r.source.ReadAt(b, int64(x.Offset)); err != nil {
		return nil, invalid("truncated extent: %v", err)
	}
	return b, nil
}

func (r *Reader) validRead(x Extent) bool { return x.Length <= maxRead && r.size.contains(x) }

func (r *Reader) sector(n lba) ([]byte, error) {
	if !n.addressable() {
		return nil, invalid("sector overflow")
	}
	return r.bytes(Extent{Offset: n.offset(), Length: uint64(sectorSize)})
}

func (r *Reader) readData(d extentData) ([]byte, error) {
	if d.size > maxRead {
		return nil, invalid("directory too large")
	}
	out := make([]byte, 0, d.size)
	for _, e := range d.extents {
		n := min(e.Length, d.size-uint64(len(out)))
		b, err := r.bytes(Extent{Offset: e.Offset, Length: n})
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
		if uint64(len(out)) == d.size {
			break
		}
	}
	if uint64(len(out)) != d.size {
		return nil, invalid("short directory")
	}
	return out, nil
}

// ucs2BE is UCS-2 big-endian text, used by Joliet and by UDF's 16-bit OSTA
// compressed names.
type ucs2BE []byte

func (b ucs2BE) String() string {
	u := make([]uint16, 0, len(b)/2)
	for len(b) >= 2 {
		u = append(u, binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	return string(utf16.Decode(u))
}

// entryName is one decoded path component as the filesystem recorded it,
// before it is checked for traversal.
type entryName string

func (n entryName) unsafe() bool {
	if n == "." || n == ".." {
		return true
	}
	return path.IsAbs(string(n)) || strings.ContainsAny(string(n), "\x00/\\")
}

// entryPath is a cleaned, slash-separated path relative to the image root;
// the root itself is the empty path.
type entryPath string

// child joins name under p, rejecting names that could escape the root.
func (p entryPath) child(name entryName) (entryPath, error) {
	if name.unsafe() {
		return "", invalid("unsafe path")
	}
	joined := string(name)
	if p != "" {
		joined = string(p) + "/" + joined
	}
	c := entryPath(path.Clean(joined))
	if c.escapesRoot() {
		return "", invalid("traversal")
	}
	return c, nil
}

func (p entryPath) escapesRoot() bool {
	return p == ".." || strings.HasPrefix(string(p), "../") || strings.HasPrefix(string(p), "/")
}

func (p entryPath) fatPath() string { return strings.ReplaceAll(string(p), "/", "\\") }

// foldKey is the name FAT32 sees once case and Unicode normalization are
// folded away.
func (p entryPath) foldKey() string { return norm.NFC.String(cases.Fold().String(string(p))) }

// treePos is the directory a walker is visiting and how deep it is nested.
type treePos struct {
	path  entryPath
	depth int
}

func (t treePos) tooDeep() bool { return t.depth > maxDepth }

// descend is the position of the subdirectory p one level below t.
func (t treePos) descend(p entryPath) treePos { return treePos{path: p, depth: t.depth + 1} }

// extentSpan is one occupied byte range and the path that owns it.
type extentSpan struct {
	start, end uint64
	path       string
}

type validator struct {
	size  imageSize
	paths map[entryPath]bool
	fold  map[string]entryPath
	spans []extentSpan
}

func (r *Reader) validate(es []Entry) error {
	v := &validator{size: r.size, paths: map[entryPath]bool{}, fold: map[string]entryPath{}}
	for _, e := range es {
		if err := v.checkEntry(e); err != nil {
			return err
		}
	}
	return v.checkSpans()
}

func (v *validator) checkEntry(e Entry) error {
	if err := v.checkPath(entryPath(e.Path)); err != nil {
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
func (v *validator) checkPath(p entryPath) error {
	if p == "" || v.paths[p] {
		return invalid("duplicate path")
	}
	v.paths[p] = true
	k := p.foldKey()
	if old, ok := v.fold[k]; ok && old != p {
		return invalid("case or Unicode collision")
	}
	v.fold[k] = p
	return nil
}

func (v *validator) collectExtents(e Entry) (uint64, error) {
	var total uint64
	for _, x := range e.Extents {
		if !v.size.contains(x) || total > x.Length+total {
			return 0, invalid("extent overflow")
		}
		total += x.Length
		if x.Length > 0 {
			v.spans = append(v.spans, extentSpan{x.Offset, x.Offset + x.Length, e.Path})
		}
	}
	return total, nil
}

// checkSpans rejects two paths claiming partially overlapping bytes. A single
// path may list adjacent or repeated extents, and different paths may share
// an identical extent: mastering tools deduplicate identical files (Windows
// oscdimg) or store hard links (xorriso) that way.
func (v *validator) checkSpans() error {
	sort.Slice(v.spans, func(i, j int) bool {
		if v.spans[i].start != v.spans[j].start {
			return v.spans[i].start < v.spans[j].start
		}
		return v.spans[i].end < v.spans[j].end
	})
	var maxEnd uint64
	var owner extentSpan
	for _, cur := range v.spans {
		if cur.start < maxEnd && conflicts(owner, cur) {
			return invalid("extent collision")
		}
		if cur.end > maxEnd {
			maxEnd, owner = cur.end, cur
		}
	}
	return nil
}

func sameExtent(a, b extentSpan) bool { return a.start == b.start && a.end == b.end }

// conflicts reports whether overlapping spans a and b may not coexist: only
// spans from the same path or identical extents are allowed to share bytes.
func conflicts(a, b extentSpan) bool { return a.path != b.path && !sameExtent(a, b) }
