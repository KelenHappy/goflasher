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
	b := &Builder{ctx: ctx, dev: device, l: l, fat: make([]uint32, l.clusters+2), next: 3,
		free: uint32(l.clusters - 1), dirs: make(map[string]*directory)}
	b.fat[0], b.fat[1], b.fat[2] = 0x0ffffff8, 0x0fffffff, 0x0fffffff
	root := &directory{cluster: 2, parent: 2, data: make([]byte, b.clusterSize()), names: make(map[string]bool)}
	copy(root.data[:11], fatLabel(label))
	root.data[11] = 0x08
	b.dirs["/"] = root
	return b, nil
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
		if cur.names[fold(part)] {
			return ErrExist
		}
		cl, err := b.allocate(0)
		if err != nil {
			return err
		}
		d := &directory{cluster: cl, parent: cur.cluster, data: make([]byte, b.clusterSize()), names: make(map[string]bool)}
		writeDot(d.data[0:32], ".", cl)
		writeDot(d.data[32:64], "..", cur.cluster)
		if err = b.addEntry(cur, part, 0x10, cl, 0); err != nil {
			b.releaseChain(cl)
			return err
		}
		b.dirs[nextKey] = d
		cur, key = d, nextKey
	}
	return nil
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
	if err != nil || len(parts) == 0 {
		if err == nil {
			err = errors.New("fat32: invalid file path")
		}
		return nil, err
	}
	d := b.dirs["/"]
	key := "/"
	for _, p := range parts[:len(parts)-1] {
		key = joinKey(key, p)
		d = b.dirs[key]
		if d == nil {
			return nil, errors.New("fat32: parent directory does not exist")
		}
	}
	base := parts[len(parts)-1]
	if d.names[fold(base)] {
		return nil, ErrExist
	}
	b.open = true
	return &File{b: b, dir: d, name: base}, nil
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
	if err := f.b.ctx.Err(); err != nil {
		f.failed = err
		return 0, err
	}
	if uint64(f.size)+uint64(len(p)) > uint64(^uint32(0)) {
		f.failed = ErrNoSpace
		return 0, ErrNoSpace
	}
	written := 0
	cs := f.b.clusterSize()
	for len(p) > 0 {
		pos := int(f.size) % cs
		if pos == 0 {
			c, err := f.b.allocate(f.last)
			if err != nil {
				f.failed = err
				return written, err
			}
			if f.first == 0 {
				f.first = c
			}
			f.last = c
		}
		n := cs - pos
		if n > len(p) {
			n = len(p)
		}
		off := f.b.clusterOffset(f.last) + int64(pos)
		// Zero the unwritten tail on first use so stale media data cannot become file data.
		if pos == 0 {
			if err := writeFullAt(f.b.ctx, f.b.dev, make([]byte, cs), f.b.clusterOffset(f.last)); err != nil {
				f.failed = err
				return written, err
			}
		}
		if err := writeFullAt(f.b.ctx, f.b.dev, p[:n], off); err != nil {
			f.failed = err
			return written, err
		}
		f.size += uint32(n)
		written += n
		p = p[n:]
	}
	return written, nil
}

func (f *File) Close() error {
	f.b.mu.Lock()
	defer f.b.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	f.closed = true
	f.b.open = false
	err := f.b.addEntry(f.dir, f.name, 0x20, f.first, f.size)
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
	fat := make([]byte, b.l.fatSectors*sectorSize)
	for i, v := range b.fat {
		if i*4+4 > len(fat) {
			break
		}
		binary.LittleEndian.PutUint32(fat[i*4:i*4+4], v)
	}
	for i := uint64(0); i < fatCount; i++ {
		if err := writeFullAt(b.ctx, b.dev, fat, int64((reserved+i*b.l.fatSectors)*sectorSize)); err != nil {
			return err
		}
	}
	for _, d := range b.dirs {
		for i, c := 0, d.cluster; c >= 2 && c < 0x0ffffff8; i++ {
			if err := writeFullAt(b.ctx, b.dev, d.data[i*b.clusterSize():(i+1)*b.clusterSize()], b.clusterOffset(c)); err != nil {
				return err
			}
			c = b.fat[c]
		}
	}
	info := fsInfoValues(b.free, b.next)
	for _, off := range []int64{512, 7 * 512} {
		if err := writeFullAt(b.ctx, b.dev, info, off); err != nil {
			return err
		}
	}
	return b.dev.Sync()
}

func (b *Builder) allocate(after uint32) (uint32, error) {
	if b.free == 0 {
		return 0, ErrNoSpace
	}
	start := b.next
	for {
		if b.next < 3 || uint64(b.next) >= uint64(len(b.fat)) {
			b.next = 3
		}
		c := b.next
		b.next++
		if b.fat[c] == 0 {
			b.fat[c] = 0x0fffffff
			if after != 0 {
				b.fat[after] = c
			}
			b.free--
			return c, nil
		}
		if b.next == start {
			return 0, ErrNoSpace
		}
	}
}
func (b *Builder) releaseChain(c uint32) {
	for c >= 2 && c < 0x0ffffff8 {
		n := b.fat[c]
		b.fat[c] = 0
		b.free++
		if c < b.next {
			b.next = c
		}
		c = n
	}
}

func (b *Builder) addEntry(d *directory, name string, attr byte, first, size uint32) error {
	alias := makeAlias(name, d.names)
	if alias == "" {
		return ErrExist
	}
	lfn := lfnEntries(name, []byte(alias))
	need := len(lfn) + 1
	end := 0
	for end+31 < len(d.data) && d.data[end] != 0 {
		end += 32
	}
	for end+need*32+32 > len(d.data) {
		oldLast := d.cluster
		for b.fat[oldLast] < 0x0ffffff8 {
			oldLast = b.fat[oldLast]
		}
		c, e := b.allocate(oldLast)
		if e != nil {
			return e
		}
		_ = c
		d.data = append(d.data, make([]byte, b.clusterSize())...)
	}
	for _, e := range lfn {
		copy(d.data[end:end+32], e)
		end += 32
	}
	e := d.data[end : end+32]
	copy(e[:11], alias)
	e[11] = attr
	setTimes(e, time.Now())
	binary.LittleEndian.PutUint16(e[20:22], uint16(first>>16))
	binary.LittleEndian.PutUint16(e[26:28], uint16(first))
	binary.LittleEndian.PutUint32(e[28:32], size)
	d.names[fold(name)] = true
	d.names[fold(alias)] = true
	return nil
}

func cleanParts(p string) ([]string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	for _, component := range strings.Split(p, "/") {
		if component == "." || component == ".." {
			return nil, errors.New("fat32: path traversal is not allowed")
		}
	}
	p = path.Clean("/" + p)
	if p == "/" {
		return nil, nil
	}
	out := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for _, s := range out {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "\x00") || len(utf16.Encode([]rune(s))) > 255 {
			return nil, errors.New("fat32: invalid path")
		}
	}
	return out, nil
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
	e[11] = 0x10
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

func makeAlias(name string, used map[string]bool) string {
	dot := strings.LastIndex(name, ".")
	base, ext := name, ""
	if dot > 0 {
		base, ext = name[:dot], name[dot+1:]
	}
	clean := func(s string) string {
		var z strings.Builder
		for _, r := range strings.ToUpper(s) {
			if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("$%'-_@~`!(){}^#&", r) {
				z.WriteRune(r)
			}
		}
		return z.String()
	}
	base, ext = clean(base), clean(ext)
	if base == "" {
		base = "FILE"
	}
	if len(ext) > 3 {
		ext = ext[:3]
	}
	pack := func(b string) string {
		return b + strings.Repeat(" ", 8-len(b)) + ext + strings.Repeat(" ", 3-len(ext))
	}
	if len(base) <= 8 {
		a := pack(base)
		if !used[fold(a)] {
			return a
		}
	}
	for n := 1; n <= 999999; n++ {
		tail := "~" + itoa(n)
		prefix := base
		if len(prefix) > 8-len(tail) {
			prefix = prefix[:8-len(tail)]
		}
		a := pack(prefix + tail)
		if !used[fold(a)] {
			return a
		}
	}
	return ""
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
		e := make([]byte, 32)
		e[0] = byte(seq)
		if seq == count {
			e[0] |= 0x40
		}
		e[11] = 0x0f
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
