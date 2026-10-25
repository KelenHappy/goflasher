package fat32

// This file implements the deliberately small filesystem construction API.  It
// is not a general FAT driver: a Builder owns a freshly formatted volume and
// only supports creating new directories and sequentially written files.

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

var (
	ErrExist   = errors.New("fat32: path already exists")
	ErrNoSpace = errors.New("fat32: device full")
	ErrClosed  = errors.New("fat32: closed")
)

const (
	attrArchive  = 0x20
	attrDir      = 0x10
	attrLabel    = 0x08
	attrLFN      = 0x0f
	entrySize    = 32
	maxAliasTail = 999999
)

// cluster is a FAT32 cluster number. It is also what each FAT entry holds: the
// next cluster of a chain, 0 when free, or an end-of-chain marker.
type cluster uint32

const (
	rootCluster cluster = 2
	firstDataCl cluster = 3 // cluster 2 is the root directory
	endOfChain  cluster = 0x0fffffff
)

// chainEnd reports whether c is an end-of-chain marker.
func (c cluster) chainEnd() bool { return c >= 0x0ffffff8 }

// Builder constructs files directly in a freshly formatted FAT32 image.  It
// never mounts the image and is consequently independent of the host OS.
type Builder struct {
	mu   sync.Mutex
	ctx  context.Context
	dev  Device
	l    layout
	fat  []cluster
	next cluster
	free uint32
	dirs map[dirKey]*directory
	open bool
}

type directory struct {
	cluster, parent cluster
	data            []byte
	names           map[foldedName]bool
}

// dirEntry is what a caller contributes to a short directory entry; the
// alias, timestamps, and long-name records are derived by addEntry.
type dirEntry struct {
	name  pathComponent
	attr  byte
	first cluster
	size  uint32
}

// NewBuilder formats device and returns a builder for the new, empty volume.
// Existing contents are always destroyed.  Overwriting paths is not supported.
func NewBuilder(ctx context.Context, device Device, size uint64, label string) (*Builder, error) {
	if err := Format(ctx, Request{Device: device, Size: size, Label: label}); err != nil {
		return nil, err
	}
	l, err := newLayout(size)
	if err != nil {
		return nil, err
	}
	b := &Builder{ctx: ctx, dev: device, l: l, fat: make([]cluster, l.clusters+2), next: firstDataCl,
		free: uint32(l.clusters - 1), dirs: make(map[dirKey]*directory)}
	b.fat[0] = 0x0ffffff8
	b.fat[1] = endOfChain
	b.fat[rootCluster] = endOfChain
	root := b.newDirectory(rootCluster, rootCluster)
	copy(root.data[:11], fatLabel(label))
	root.data[11] = attrLabel
	b.dirs[rootKey] = root
	return b, nil
}

func (b *Builder) newDirectory(first, parent cluster) *directory {
	return &directory{cluster: first, parent: parent, data: make([]byte, b.clusterSize()), names: make(map[foldedName]bool)}
}

func (b *Builder) clusterSize() int { return int(b.l.sectorsPerCluster * sectorSize) }
func (b *Builder) clusterOffset(c cluster) int64 {
	return int64((reserved + fatCount*b.l.fatSectors + uint64(c-2)*b.l.sectorsPerCluster) * sectorSize)
}

// MkdirAll creates path and missing parents. Paths are slash-separated and
// comparisons are Unicode case-insensitive. Every subdirectory gets . and ...
func (b *Builder) MkdirAll(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ctx.Err(); err != nil {
		return err
	}
	parts, err := slashPath(name).components()
	if err != nil {
		return err
	}
	cur, key := b.dirs[rootKey], rootKey
	for _, part := range parts {
		nextKey := key.child(part)
		if d := b.dirs[nextKey]; d != nil {
			cur, key = d, nextKey
			continue
		}
		d, err := b.mkdir(cur, part)
		if err != nil {
			return err
		}
		b.dirs[nextKey] = d
		cur, key = d, nextKey
	}
	return nil
}

func (b *Builder) mkdir(parent *directory, name pathComponent) (*directory, error) {
	if parent.names[name.folded()] {
		return nil, ErrExist
	}
	cl, err := b.allocate(0)
	if err != nil {
		return nil, err
	}
	d := b.newDirectory(cl, parent.cluster)
	entrySlot(d.data[0:entrySize]).setDot(dotSelf, cl)
	entrySlot(d.data[entrySize:2*entrySize]).setDot(dotParent, parent.cluster)
	if err = b.addEntry(parent, dirEntry{name: name, attr: attrDir, first: cl}); err != nil {
		b.releaseChain(cl)
		return nil, err
	}
	return d, nil
}

// Create creates a new file for sequential writes. The caller must Close it.
// It returns ErrExist for case-insensitive long-name or generated-alias clashes.
func (b *Builder) Create(name string) (*File, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ctx.Err(); err != nil {
		return nil, err
	}
	parts, err := slashPath(name).components()
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, errors.New("fat32: invalid file path")
	}
	d, err := b.lookupDir(parts[:len(parts)-1])
	if err != nil {
		return nil, err
	}
	base := parts[len(parts)-1]
	if d.names[base.folded()] {
		return nil, ErrExist
	}
	b.open = true
	return &File{b: b, dir: d, name: base}, nil
}

func (b *Builder) lookupDir(parts []pathComponent) (*directory, error) {
	key := rootKey
	for _, p := range parts {
		key = key.child(p)
		if b.dirs[key] == nil {
			return nil, errors.New("fat32: parent directory does not exist")
		}
	}
	return b.dirs[key], nil
}

// File is a sequential writer. Close publishes its directory entry, including
// a partial file after a Write returned an error. Close does not call Sync.
type File struct {
	b           *Builder
	dir         *directory
	name        pathComponent
	first, last cluster
	size        uint32
	closed      bool
	failed      error
}

func (f *File) Write(p []byte) (int, error) {
	f.b.mu.Lock()
	defer f.b.mu.Unlock()
	if f.closed {
		return 0, ErrClosed
	}
	if err := f.checkWritable(len(p)); err != nil {
		return 0, err
	}
	written := 0
	for len(p) > 0 {
		n, err := f.writeRun(p)
		written += n
		if err != nil {
			return written, f.fail(err)
		}
		p = p[n:]
	}
	return written, nil
}

func (f *File) checkWritable(n int) error {
	if err := f.b.ctx.Err(); err != nil {
		return f.fail(err)
	}
	if uint64(f.size)+uint64(n) > uint64(^uint32(0)) {
		return f.fail(ErrNoSpace)
	}
	return nil
}

func (f *File) fail(err error) error {
	f.failed = err
	return err
}

// writeRun writes the longest prefix of p that is contiguous on the device:
// the rest of the current cluster, then every newly allocated cluster that
// directly follows it. Each run is one device write, so a large Write costs a
// handful of requests rather than one per cluster.
func (f *File) writeRun(p []byte) (int, error) {
	cs := f.b.clusterSize()
	pos := int(f.size) % cs
	if pos == 0 {
		if err := f.openCluster(); err != nil {
			return 0, err
		}
	}
	start := f.b.clusterOffset(f.last) + int64(pos)
	n := f.growRun(min(cs-pos, len(p)), len(p))
	if err := writeFullAt(f.b.ctx, f.b.dev, p[:n], start); err != nil {
		return 0, err
	}
	if err := f.zeroTail(start+int64(n), (pos+n)%cs); err != nil {
		return 0, err
	}
	f.size += uint32(n)
	return n, nil
}

// growRun extends a run of n bytes that ends in f.last with the clusters
// directly after it, until the run covers want bytes or the next cluster
// cannot be claimed, and returns the run's new length.
func (f *File) growRun(n, want int) int {
	cs := f.b.clusterSize()
	for n < want {
		c, ok := f.b.allocateAdjacent(f.last)
		if !ok {
			return n
		}
		f.last = c
		n += min(cs, want-n)
	}
	return n
}

// zeroTail clears the rest of a cluster that a run filled only up to used
// bytes, so stale media data cannot become file data in the unwritten tail.
// A later Write overwrites the zeros it continues into.
func (f *File) zeroTail(off int64, used int) error {
	if used == 0 {
		return nil
	}
	return writeFullAt(f.b.ctx, f.b.dev, make([]byte, f.b.clusterSize()-used), off)
}

// openCluster appends a cluster, at any free position, to the chain.
func (f *File) openCluster() error {
	c, err := f.b.allocate(f.last)
	if err != nil {
		return err
	}
	if f.first == 0 {
		f.first = c
	}
	f.last = c
	return nil
}

func (f *File) Close() error {
	f.b.mu.Lock()
	defer f.b.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	f.closed = true
	f.b.open = false
	err := f.b.addEntry(f.dir, dirEntry{name: f.name, attr: attrArchive, first: f.first, size: f.size})
	if err != nil {
		f.b.releaseChain(f.first)
		return err
	}
	return f.failed
}

// Sync writes both FAT mirrors, every directory, both FSInfo copies, and then
// flushes the device. It is invalid while a file is open.
func (b *Builder) Sync() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open {
		return errors.New("fat32: file is still open")
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if err := b.writeFATs(); err != nil {
		return err
	}
	if err := b.writeDirectories(); err != nil {
		return err
	}
	if err := b.writeFSInfo(); err != nil {
		return err
	}
	return b.dev.Sync()
}

func (b *Builder) encodeFAT() []byte {
	fat := make([]byte, b.l.fatSectors*sectorSize)
	for i, v := range b.fat {
		if i*4+4 > len(fat) {
			break
		}
		binary.LittleEndian.PutUint32(fat[i*4:i*4+4], uint32(v))
	}
	return fat
}

func (b *Builder) writeFATs() error {
	fat := b.encodeFAT()
	for i := uint64(0); i < fatCount; i++ {
		if err := writeFullAt(b.ctx, b.dev, fat, int64((reserved+i*b.l.fatSectors)*sectorSize)); err != nil {
			return err
		}
	}
	return nil
}

func (b *Builder) writeDirectories() error {
	for _, d := range b.dirs {
		if err := b.writeDirectory(d); err != nil {
			return err
		}
	}
	return nil
}

// writeDirectory scatters the directory's data across its cluster chain.
func (b *Builder) writeDirectory(d *directory) error {
	cs := b.clusterSize()
	for i, c := 0, d.cluster; c >= 2 && !c.chainEnd(); i++ {
		if err := writeFullAt(b.ctx, b.dev, d.data[i*cs:(i+1)*cs], b.clusterOffset(c)); err != nil {
			return err
		}
		c = b.fat[c]
	}
	return nil
}

func (b *Builder) writeFSInfo() error {
	info := b.fsInfo()
	for _, off := range []int64{512, 7 * 512} {
		if err := writeFullAt(b.ctx, b.dev, info, off); err != nil {
			return err
		}
	}
	return nil
}

// allocate claims a free cluster and, when after is non-zero, links it to
// the end of that chain.
func (b *Builder) allocate(after cluster) (cluster, error) {
	if b.free == 0 {
		return 0, ErrNoSpace
	}
	c, ok := b.findFree()
	if !ok {
		return 0, ErrNoSpace
	}
	b.fat[c] = endOfChain
	if after != 0 {
		b.fat[after] = c
	}
	b.free--
	return c, nil
}

// allocateAdjacent claims the cluster directly after c and links it to c,
// reporting false when that cluster is past the end of the volume or in use.
func (b *Builder) allocateAdjacent(c cluster) (cluster, bool) {
	n := c + 1
	if b.free == 0 || !b.isFree(n) {
		return 0, false
	}
	b.fat[n] = endOfChain
	b.fat[c] = n
	b.free--
	if b.next == n {
		b.next++
	}
	return n, true
}

// isFree reports whether c is inside the volume and not part of any chain.
func (b *Builder) isFree(c cluster) bool {
	return uint64(c) < uint64(len(b.fat)) && b.fat[c] == 0
}

// findFree scans forward from the next-free hint, wrapping once.
func (b *Builder) findFree() (cluster, bool) {
	start := b.next
	for {
		c := b.takeNext()
		if b.fat[c] == 0 {
			return c, true
		}
		if b.next == start {
			return 0, false
		}
	}
}

func (b *Builder) takeNext() cluster {
	if b.next < firstDataCl || uint64(b.next) >= uint64(len(b.fat)) {
		b.next = firstDataCl
	}
	c := b.next
	b.next++
	return c
}

func (b *Builder) releaseChain(c cluster) {
	for c >= 2 && !c.chainEnd() {
		n := b.fat[c]
		b.fat[c] = 0
		b.free++
		if c < b.next {
			b.next = c
		}
		c = n
	}
}

func (b *Builder) addEntry(d *directory, e dirEntry) error {
	alias := d.alias(e.name)
	if alias == "" {
		return ErrExist
	}
	lfn := e.name.lfnEntries(alias)
	end := d.used()
	if err := b.growDirectory(d, lfn); err != nil {
		return err
	}
	for _, rec := range lfn {
		copy(d.data[end:end+entrySize], rec)
		end += entrySize
	}
	entrySlot(d.data[end:end+entrySize]).setShort(alias, e)
	d.names[e.name.folded()] = true
	d.names[alias.folded()] = true
	return nil
}

// used returns the offset of the first unused directory slot.
func (d *directory) used() int {
	end := 0
	for end+entrySize-1 < len(d.data) && d.data[end] != 0 {
		end += entrySize
	}
	return end
}

// growDirectory extends d's cluster chain until it holds records and their
// short entry after the used slots, plus one trailing free slot, which marks
// the end of the directory.
func (b *Builder) growDirectory(d *directory, records []lfnRecord) error {
	need := d.used() + (len(records)+1)*entrySize
	for need+entrySize > len(d.data) {
		if _, err := b.allocate(b.lastCluster(d.cluster)); err != nil {
			return err
		}
		d.data = append(d.data, make([]byte, b.clusterSize())...)
	}
	return nil
}

func (b *Builder) lastCluster(c cluster) cluster {
	for !b.fat[c].chainEnd() {
		c = b.fat[c]
	}
	return c
}

// entrySlot is one 32-byte directory entry.
type entrySlot []byte

// The . and .. entries, packed like short aliases.
const (
	dotSelf   shortAlias = ".          "
	dotParent shortAlias = "..         "
)

// setShort writes the short directory entry for e under alias.
func (s entrySlot) setShort(alias shortAlias, e dirEntry) {
	copy(s[:11], alias)
	s[11] = e.attr
	s.setTimes(time.Now())
	binary.LittleEndian.PutUint16(s[20:22], uint16(e.first>>16))
	binary.LittleEndian.PutUint16(s[26:28], uint16(e.first))
	binary.LittleEndian.PutUint32(s[28:32], e.size)
}

// setDot writes a . or .. entry that points at c.
func (s entrySlot) setDot(name shortAlias, c cluster) {
	copy(s[:11], name)
	s[11] = attrDir
	binary.LittleEndian.PutUint16(s[20:22], uint16(c>>16))
	binary.LittleEndian.PutUint16(s[26:28], uint16(c))
}

func (s entrySlot) setTimes(t time.Time) {
	y, m, d := t.Date()
	if y < 1980 {
		y = 1980
	}
	if y > 2107 {
		y = 2107
	}
	date := uint16(y-1980)<<9 | uint16(m)<<5 | uint16(d)
	tm := uint16(t.Hour())<<11 | uint16(t.Minute())<<5 | uint16(t.Second()/2)
	binary.LittleEndian.PutUint16(s[14:16], tm)
	binary.LittleEndian.PutUint16(s[16:18], date)
	binary.LittleEndian.PutUint16(s[18:20], date)
	binary.LittleEndian.PutUint16(s[22:24], tm)
	binary.LittleEndian.PutUint16(s[24:26], date)
}

// slashPath is a caller's file or directory path. Backslashes also separate
// components.
type slashPath string

// components validates p and splits it into path components, rejecting
// traversal and names FAT cannot store. The root yields none.
func (p slashPath) components() ([]pathComponent, error) {
	normalized := slashPath(strings.ReplaceAll(string(p), "\\", "/"))
	if normalized.traversal() {
		return nil, errors.New("fat32: path traversal is not allowed")
	}
	clean := path.Clean("/" + string(normalized))
	if clean == "/" {
		return nil, nil
	}
	var out []pathComponent
	for _, s := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		c := pathComponent(s)
		if !c.valid() {
			return nil, errors.New("fat32: invalid path")
		}
		out = append(out, c)
	}
	return out, nil
}

// traversal reports whether any component of p is . or ...
func (p slashPath) traversal() bool {
	for _, c := range strings.Split(string(p), "/") {
		if pathComponent(c).isDot() {
			return true
		}
	}
	return false
}

// pathComponent is one element of a slashPath, which is also the long name of
// the entry it creates.
type pathComponent string

// isDot reports whether c is one of the reserved "." or ".." entries.
func (c pathComponent) isDot() bool {
	return c == "." || c == ".."
}

func (c pathComponent) valid() bool {
	if c == "" || c.isDot() {
		return false
	}
	if strings.ContainsRune(string(c), 0) {
		return false
	}
	return len(utf16.Encode([]rune(string(c)))) <= 255
}

// foldedName is a name in the case-insensitive form directories compare.
type foldedName string

func (c pathComponent) folded() foldedName { return foldedName(strings.ToUpper(string(c))) }

// dirKey indexes Builder.dirs by the folded path from the root.
type dirKey string

const rootKey dirKey = "/"

func (k dirKey) child(name pathComponent) dirKey {
	if k == rootKey {
		return "/" + dirKey(name.folded())
	}
	return k + "/" + dirKey(name.folded())
}

// shortAlias is a packed 8.3 short name: an eight-character base and a
// three-character extension, both space-padded.
type shortAlias string

func (a shortAlias) folded() foldedName { return foldedName(strings.ToUpper(string(a))) }

// checksum is the value every long-name record of the entry repeats.
func (a shortAlias) checksum() byte {
	var s byte
	for i := 0; i < 11; i++ {
		s = ((s & 1) << 7) + (s >> 1) + a[i]
	}
	return s
}

// alias derives an 8.3 short name for name that does not collide with any
// name already in d, numbering with ~N when the plain form is taken.
// It returns "" when every numbered form is exhausted.
func (d *directory) alias(name pathComponent) shortAlias {
	parts := name.aliasParts()
	if a, ok := d.freeAlias(parts); ok {
		return a
	}
	for n := aliasTail(1); n <= maxAliasTail; n++ {
		if a, ok := d.freeAlias(parts.numbered(n)); ok {
			return a
		}
	}
	return ""
}

// freeAlias packs parts into an 8.3 alias and reports whether that alias fits
// the short-name format and is still unused in d.
func (d *directory) freeAlias(parts aliasParts) (shortAlias, bool) {
	if len(parts.base) > 8 {
		return "", false
	}
	a := parts.pack()
	return a, !d.names[a.folded()]
}

// aliasParts is the base and extension of a short name, before packing.
type aliasParts struct{ base, ext string }

// aliasParts reduces c to the characters FAT permits in a short name, with
// the extension truncated to three and an empty base replaced by FILE.
func (c pathComponent) aliasParts() aliasParts {
	name := string(c)
	base, ext := name, ""
	if dot := strings.LastIndex(name, "."); dot > 0 {
		base, ext = name[:dot], name[dot+1:]
	}
	p := aliasParts{base: aliasChars(base), ext: aliasChars(ext)}
	if p.base == "" {
		p.base = "FILE"
	}
	if len(p.ext) > 3 {
		p.ext = p.ext[:3]
	}
	return p
}

// aliasTail is the N of a numbered ~N alias.
type aliasTail int

// numbered shortens the base so that ~n still fits in eight characters.
func (p aliasParts) numbered(n aliasTail) aliasParts {
	tail := "~" + strconv.Itoa(int(n))
	if len(p.base) > 8-len(tail) {
		p.base = p.base[:8-len(tail)]
	}
	p.base += tail
	return p
}

func (p aliasParts) pack() shortAlias {
	return shortAlias(p.base + strings.Repeat(" ", 8-len(p.base)) + p.ext + strings.Repeat(" ", 3-len(p.ext)))
}

// aliasRunes is the set of characters FAT permits in an 8.3 short name.
const aliasRunes = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789$%'-_@~`!(){}^#&"

func aliasChars(s string) string {
	var z strings.Builder
	for _, r := range strings.ToUpper(s) {
		if strings.ContainsRune(aliasRunes, r) {
			z.WriteRune(r)
		}
	}
	return z.String()
}

// lfnRecord is one long-file-name directory entry.
type lfnRecord []byte

// lfnEntries encodes c as long-name records, last part first, each carrying
// the checksum of alias.
func (c pathComponent) lfnEntries(alias shortAlias) []lfnRecord {
	u := utf16.Encode([]rune(string(c)))
	u = append(u, 0)
	for len(u)%13 != 0 {
		u = append(u, 0xffff)
	}
	count := len(u) / 13
	out := make([]lfnRecord, 0, count)
	sum := alias.checksum()
	for seq := count; seq >= 1; seq-- {
		e := make(lfnRecord, entrySize)
		e[0] = byte(seq)
		if seq == count {
			e[0] |= 0x40
		}
		e[11] = attrLFN
		e[13] = sum
		chunk := u[(seq-1)*13 : seq*13]
		pos := []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}
		for i, v := range chunk {
			binary.LittleEndian.PutUint16(e[pos[i]:pos[i]+2], v)
		}
		out = append(out, e)
	}
	return out
}

// fsInfo is the FSInfo sector with the current free count and next-free hint.
func (b *Builder) fsInfo() []byte {
	info := make([]byte, 512)
	binary.LittleEndian.PutUint32(info[0:4], 0x41615252)
	binary.LittleEndian.PutUint32(info[484:488], 0x61417272)
	binary.LittleEndian.PutUint32(info[488:492], b.free)
	binary.LittleEndian.PutUint32(info[492:496], uint32(b.next))
	binary.LittleEndian.PutUint32(info[508:512], 0xaa550000)
	return info
}

var _ io.WriteCloser = (*File)(nil)
