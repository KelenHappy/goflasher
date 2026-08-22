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
	endOfChain   = 0x0fffffff
	firstDataCl  = 3 // cluster 2 is the root directory
	attrArchive  = 0x20
	attrDir      = 0x10
	attrLabel    = 0x08
	attrLFN      = 0x0f
	entrySize    = 32
	maxAliasTail = 999999
)

func isChainEnd(c uint32) bool { return c >= 0x0ffffff8 }

// Builder constructs files directly in a freshly formatted FAT32 image.  It
// never mounts the image and is consequently independent of the host OS.
type Builder struct {
	mu         sync.Mutex
	ctx        context.Context
	dev        Device
	l          layout
	fat        []uint32
	next, free uint32
	dirs       map[string]*directory
	open       bool
}

type directory struct {
	cluster, parent uint32
	data            []byte
	names           map[string]bool
}

// dirEntry is what a caller contributes to a short directory entry; the
// alias, timestamps, and long-name records are derived by addEntry.
type dirEntry struct {
	name        string
	attr        byte
	first, size uint32
}

// NewBuilder formats device and returns a builder for the new, empty volume.
// Existing contents are always destroyed.  Overwriting paths is not supported.
func NewBuilder(ctx context.Context, device Device, size uint64, label string) (*Builder, error) {
	if err := Format(ctx, device, size, label, nil); err != nil {
		return nil, err
	}
	l, err := newLayout(size)
	if err != nil {
		return nil, err
	}
	b := &Builder{ctx: ctx, dev: device, l: l, fat: make([]uint32, l.clusters+2), next: firstDataCl,
		free: uint32(l.clusters - 1), dirs: make(map[string]*directory)}
	b.fat[0], b.fat[1], b.fat[2] = 0x0ffffff8, endOfChain, endOfChain
	root := b.newDirectory(2, 2)
	copy(root.data[:11], fatLabel(label))
	root.data[11] = attrLabel
	b.dirs["/"] = root
	return b, nil
}

func (b *Builder) newDirectory(cluster, parent uint32) *directory {
	return &directory{cluster: cluster, parent: parent, data: make([]byte, b.clusterSize()), names: make(map[string]bool)}
}

func (b *Builder) clusterSize() int { return int(b.l.sectorsPerCluster * sectorSize) }
func (b *Builder) clusterOffset(c uint32) int64 {
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
	parts, err := cleanParts(name)
	if err != nil {
		return err
	}
	cur, key := b.dirs["/"], "/"
	for _, part := range parts {
		nextKey := joinKey(key, part)
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

func (b *Builder) mkdir(parent *directory, name string) (*directory, error) {
	if parent.names[fold(name)] {
		return nil, ErrExist
	}
	cl, err := b.allocate(0)
	if err != nil {
		return nil, err
	}
	d := b.newDirectory(cl, parent.cluster)
	writeDot(d.data[0:entrySize], ".", cl)
	writeDot(d.data[entrySize:2*entrySize], "..", parent.cluster)
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
	parts, err := cleanParts(name)
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
	if d.names[fold(base)] {
		return nil, ErrExist
	}
	b.open = true
	return &File{b: b, dir: d, name: base}, nil
}

func (b *Builder) lookupDir(parts []string) (*directory, error) {
	key := "/"
	for _, p := range parts {
		key = joinKey(key, p)
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
	name        string
	first, last uint32
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
		n, err := f.writeChunk(p)
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

// writeChunk writes as much of p as fits in the current cluster, allocating
// and zeroing a new one at a cluster boundary.
func (f *File) writeChunk(p []byte) (int, error) {
	cs := f.b.clusterSize()
	pos := int(f.size) % cs
	if pos == 0 {
		if err := f.openCluster(); err != nil {
			return 0, err
		}
	}
	n := min(cs-pos, len(p))
	if err := writeFullAt(f.b.ctx, f.b.dev, p[:n], f.b.clusterOffset(f.last)+int64(pos)); err != nil {
		return 0, err
	}
	f.size += uint32(n)
	return n, nil
}

// openCluster appends a cluster to the chain and zeroes it so stale media
// data cannot become file data in the unwritten tail.
func (f *File) openCluster() error {
	c, err := f.b.allocate(f.last)
	if err != nil {
		return err
	}
	if f.first == 0 {
		f.first = c
	}
	f.last = c
	return writeFullAt(f.b.ctx, f.b.dev, make([]byte, f.b.clusterSize()), f.b.clusterOffset(c))
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
		binary.LittleEndian.PutUint32(fat[i*4:i*4+4], v)
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
	for i, c := 0, d.cluster; c >= 2 && !isChainEnd(c); i++ {
		if err := writeFullAt(b.ctx, b.dev, d.data[i*cs:(i+1)*cs], b.clusterOffset(c)); err != nil {
			return err
		}
		c = b.fat[c]
	}
	return nil
}

func (b *Builder) writeFSInfo() error {
	info := fsInfoValues(b.free, b.next)
	for _, off := range []int64{512, 7 * 512} {
		if err := writeFullAt(b.ctx, b.dev, info, off); err != nil {
			return err
		}
	}
	return nil
}

// allocate claims a free cluster and, when after is non-zero, links it to
// the end of that chain.
func (b *Builder) allocate(after uint32) (uint32, error) {
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

// findFree scans forward from the next-free hint, wrapping once.
func (b *Builder) findFree() (uint32, bool) {
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

func (b *Builder) takeNext() uint32 {
	if b.next < firstDataCl || uint64(b.next) >= uint64(len(b.fat)) {
		b.next = firstDataCl
	}
	c := b.next
	b.next++
	return c
}

func (b *Builder) releaseChain(c uint32) {
	for c >= 2 && !isChainEnd(c) {
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
	alias := makeAlias(e.name, d.names)
	if alias == "" {
		return ErrExist
	}
	lfn := lfnEntries(e.name, []byte(alias))
	end := d.used()
	if err := b.growDirectory(d, end+(len(lfn)+1)*entrySize); err != nil {
		return err
	}
	for _, rec := range lfn {
		copy(d.data[end:end+entrySize], rec)
		end += entrySize
	}
	encodeShortEntry(d.data[end:end+entrySize], alias, e)
	d.names[fold(e.name)] = true
	d.names[fold(alias)] = true
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

// growDirectory extends d's cluster chain until it holds at least need bytes
// plus one trailing free slot, which marks the end of the directory.
func (b *Builder) growDirectory(d *directory, need int) error {
	for need+entrySize > len(d.data) {
		if _, err := b.allocate(b.lastCluster(d.cluster)); err != nil {
			return err
		}
		d.data = append(d.data, make([]byte, b.clusterSize())...)
	}
	return nil
}

func (b *Builder) lastCluster(c uint32) uint32 {
	for !isChainEnd(b.fat[c]) {
		c = b.fat[c]
	}
	return c
}

func encodeShortEntry(buf []byte, alias string, e dirEntry) {
	copy(buf[:11], alias)
	buf[11] = e.attr
	setTimes(buf, time.Now())
	binary.LittleEndian.PutUint16(buf[20:22], uint16(e.first>>16))
	binary.LittleEndian.PutUint16(buf[26:28], uint16(e.first))
	binary.LittleEndian.PutUint32(buf[28:32], e.size)
}

func cleanParts(p string) ([]string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	if hasTraversal(p) {
		return nil, errors.New("fat32: path traversal is not allowed")
	}
	p = path.Clean("/" + p)
	if p == "/" {
		return nil, nil
	}
	out := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for _, s := range out {
		if !validComponent(s) {
			return nil, errors.New("fat32: invalid path")
		}
	}
	return out, nil
}

func hasTraversal(p string) bool {
	for _, component := range strings.Split(p, "/") {
		if component == "." || component == ".." {
			return true
		}
	}
	return false
}

func validComponent(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.ContainsRune(s, 0) {
		return false
	}
	return len(utf16.Encode([]rune(s))) <= 255
}

func joinKey(parent, name string) string {
	if parent == "/" {
		return "/" + fold(name)
	}
	return parent + "/" + fold(name)
}
func fold(s string) string { return strings.ToUpper(s) }

func writeDot(e []byte, name string, c uint32) {
	copy(e[:11], "           ")
	copy(e, name)
	e[11] = attrDir
	binary.LittleEndian.PutUint16(e[20:22], uint16(c>>16))
	binary.LittleEndian.PutUint16(e[26:28], uint16(c))
}
func setTimes(e []byte, t time.Time) {
	y, m, d := t.Date()
	if y < 1980 {
		y = 1980
	}
	if y > 2107 {
		y = 2107
	}
	date := uint16(y-1980)<<9 | uint16(m)<<5 | uint16(d)
	tm := uint16(t.Hour())<<11 | uint16(t.Minute())<<5 | uint16(t.Second()/2)
	binary.LittleEndian.PutUint16(e[14:16], tm)
	binary.LittleEndian.PutUint16(e[16:18], date)
	binary.LittleEndian.PutUint16(e[18:20], date)
	binary.LittleEndian.PutUint16(e[22:24], tm)
	binary.LittleEndian.PutUint16(e[24:26], date)
}

// makeAlias derives an 8.3 short name that does not collide with any name
// already in the directory, numbering with ~N when the plain form is taken.
// It returns "" when every numbered form is exhausted.
func makeAlias(name string, used map[string]bool) string {
	base, ext := splitAliasParts(name)
	if len(base) <= 8 {
		if a := packAlias(base, ext); !used[fold(a)] {
			return a
		}
	}
	for n := 1; n <= maxAliasTail; n++ {
		if a := packAlias(numberedBase(base, n), ext); !used[fold(a)] {
			return a
		}
	}
	return ""
}

// splitAliasParts reduces name to the characters FAT permits in a short name,
// with the extension truncated to three and an empty base replaced by FILE.
func splitAliasParts(name string) (base, ext string) {
	base = name
	if dot := strings.LastIndex(name, "."); dot > 0 {
		base, ext = name[:dot], name[dot+1:]
	}
	base, ext = aliasChars(base), aliasChars(ext)
	if base == "" {
		base = "FILE"
	}
	if len(ext) > 3 {
		ext = ext[:3]
	}
	return base, ext
}

func aliasChars(s string) string {
	var z strings.Builder
	for _, r := range strings.ToUpper(s) {
		if isAliasRune(r) {
			z.WriteRune(r)
		}
	}
	return z.String()
}

func isAliasRune(r rune) bool {
	if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
		return true
	}
	return strings.ContainsRune("$%'-_@~`!(){}^#&", r)
}

func numberedBase(base string, n int) string {
	tail := "~" + itoa(n)
	if len(base) > 8-len(tail) {
		base = base[:8-len(tail)]
	}
	return base + tail
}

func packAlias(base, ext string) string {
	return base + strings.Repeat(" ", 8-len(base)) + ext + strings.Repeat(" ", 3-len(ext))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
func lfnEntries(name string, alias []byte) [][]byte {
	u := utf16.Encode([]rune(name))
	u = append(u, 0)
	for len(u)%13 != 0 {
		u = append(u, 0xffff)
	}
	count := len(u) / 13
	out := make([][]byte, 0, count)
	sum := lfnChecksum(alias)
	for seq := count; seq >= 1; seq-- {
		e := make([]byte, entrySize)
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
func lfnChecksum(a []byte) byte {
	var s byte
	for i := 0; i < 11; i++ {
		s = ((s & 1) << 7) + (s >> 1) + a[i]
	}
	return s
}
func fsInfoValues(free, next uint32) []byte {
	b := make([]byte, 512)
	binary.LittleEndian.PutUint32(b[0:4], 0x41615252)
	binary.LittleEndian.PutUint32(b[484:488], 0x61417272)
	binary.LittleEndian.PutUint32(b[488:492], free)
	binary.LittleEndian.PutUint32(b[492:496], next)
	binary.LittleEndian.PutUint32(b[508:512], 0xaa550000)
	return b
}

var _ io.WriteCloser = (*File)(nil)
