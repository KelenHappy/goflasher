package fat32

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

type memoryDevice struct {
	b        []byte
	synced   int
	maxWrite int
}

func (m *memoryDevice) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(m.b)) {
		return 0, io.ErrShortWrite
	}
	if m.maxWrite > 0 && len(p) > m.maxWrite {
		p = p[:m.maxWrite]
	}
	n := copy(m.b[off:], p)
	if n != len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}
func (m *memoryDevice) Sync() error { m.synced++; return nil }

func TestBuilderCreatesParseableTreeAndChains(t *testing.T) {
	const size = 64 << 20
	d := &memoryDevice{b: make([]byte, size), maxWrite: 137} // exercise short successful writes
	b, err := NewBuilder(context.Background(), d, size, "GOFLASHER")
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte("cluster-chain-data"), b.clusterSize()/4)
	populateTree(t, b, want)
	if err = b.Sync(); err != nil {
		t.Fatal(err)
	}
	if d.synced < 2 { // format and final sync
		t.Fatalf("sync count=%d", d.synced)
	}
	image := testImage(d.b)
	image.assertFATMirrors(t)
	image.assertTree(t, want)
	image.assertFSInfo(t, b)
}

func populateTree(t *testing.T, b *Builder, data []byte) {
	t.Helper()
	if err := b.MkdirAll("EFI/啟動工具"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, b, "EFI/啟動工具/Very Long Boot Filename.bin", data)
	writeFile(t, b, "EFI/empty.txt", nil)
	if _, err := b.Create("efi/EMPTY.TXT"); !errors.Is(err, ErrExist) {
		t.Fatalf("collision error=%v", err)
	}
}

func writeFile(t *testing.T, b *Builder, name string, data []byte) {
	t.Helper()
	f, err := b.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, f, data)
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, f *File, data []byte) {
	t.Helper()
	if n, err := f.Write(data); err != nil || n != len(data) {
		t.Fatalf("Write=%d, %v", n, err)
	}
}

// testImage is a formatted FAT32 volume as raw bytes. Every method reads the
// geometry from the image's own boot sector rather than from the Builder.
type testImage []byte

func (img testImage) sectorsPerCluster() uint32 { return uint32(img[13]) }
func (img testImage) reservedSectors() uint32   { return uint32(binary.LittleEndian.Uint16(img[14:16])) }
func (img testImage) sectorsPerFAT() uint32     { return binary.LittleEndian.Uint32(img[36:40]) }

// fat returns the first FAT copy, or the mirror when copy is 1.
func (img testImage) fat(copy uint32) []byte {
	start := (img.reservedSectors() + copy*img.sectorsPerFAT()) * 512
	return img[start : start+img.sectorsPerFAT()*512]
}

func (img testImage) assertFATMirrors(t *testing.T) {
	t.Helper()
	if !bytes.Equal(img.fat(0), img.fat(1)) {
		t.Fatal("FAT mirrors differ")
	}
}

// readChain concatenates the clusters of the chain that starts at cluster.
func (img testImage) readChain(t *testing.T, cluster uint32) []byte {
	t.Helper()
	spc := img.sectorsPerCluster()
	dataStart := img.reservedSectors() + 2*img.sectorsPerFAT()
	fat := img.fat(0)
	var out []byte
	seen := map[uint32]bool{}
	for cluster >= 2 && cluster < 0x0ffffff8 {
		if seen[cluster] {
			t.Fatal("FAT loop")
		}
		seen[cluster] = true
		off := (dataStart + (cluster-2)*spc) * 512
		out = append(out, img[off:off+spc*512]...)
		cluster = binary.LittleEndian.Uint32(fat[cluster*4:cluster*4+4]) & 0x0fffffff
	}
	return out
}

// root is the root directory, which this builder always places at cluster 2.
func (img testImage) root(t *testing.T) rawDir {
	t.Helper()
	return rawDir(img.readChain(t, 2))
}

// subdirectory reads the directory that entry names.
func (img testImage) subdirectory(t *testing.T, entry rawEntry) rawDir {
	t.Helper()
	return rawDir(img.readChain(t, entry.cluster()))
}

func (img testImage) assertTree(t *testing.T, want []byte) {
	t.Helper()
	efi := img.subdirectory(t, img.root(t).find(t, "EFI"))
	if string(efi[:1]) != "." || string(efi[32:34]) != ".." {
		t.Fatal("missing dot entries")
	}
	long := img.subdirectory(t, efi.find(t, "啟動工具"))
	img.assertFileContents(t, long.find(t, "Very Long Boot Filename.bin"), want)
	if empty := efi.find(t, "empty.txt"); empty.cluster() != 0 || empty.fileSize() != 0 {
		t.Fatal("empty file allocated a cluster")
	}
}

func (img testImage) assertFileContents(t *testing.T, entry rawEntry, want []byte) {
	t.Helper()
	if got := entry.fileSize(); got != uint32(len(want)) {
		t.Fatalf("size=%d", got)
	}
	if got := img.readChain(t, entry.cluster())[:len(want)]; !bytes.Equal(got, want) {
		t.Fatal("cross-cluster contents differ")
	}
}

func (img testImage) assertFSInfo(t *testing.T, b *Builder) {
	t.Helper()
	info := img[512:1024]
	if binary.LittleEndian.Uint32(info[488:492]) != b.free || binary.LittleEndian.Uint32(info[492:496]) != b.next {
		t.Fatal("FSInfo was not updated")
	}
}

func TestBuilderDeviceFullFinalizesPartialFile(t *testing.T) {
	d := &memoryDevice{b: make([]byte, 64<<20)}
	b, err := NewBuilder(context.Background(), d, uint64(len(d.b)), "TEST")
	if err != nil {
		t.Fatal(err)
	}
	b.free = 1 // simulate an image with only one free data cluster
	f, err := b.Create("partial.bin")
	if err != nil {
		t.Fatal(err)
	}
	p := make([]byte, b.clusterSize()+1)
	n, err := f.Write(p)
	if !errors.Is(err, ErrNoSpace) || n != b.clusterSize() {
		t.Fatalf("Write=%d,%v", n, err)
	}
	if err = f.Close(); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("Close=%v", err)
	}
	if err = b.Sync(); err != nil {
		t.Fatal(err)
	}
	e := testImage(d.b).root(t).find(t, "partial.bin")
	if e.fileSize() != uint32(n) {
		t.Fatal("partial size not finalized")
	}
}

// rawDir is the on-disk contents of one directory's cluster chain.
type rawDir []byte

// find returns the entry named want, matching its long name when it has one
// and its 8.3 short name otherwise, case-insensitively.
func (d rawDir) find(t *testing.T, want string) rawEntry {
	t.Helper()
	var name longName
	for off := 0; off+32 <= len(d) && d[off] != 0; off += 32 {
		e := rawEntry(d[off : off+32])
		if e.isLongNamePart() {
			name = append(longName{e.longNameChars()}, name...)
			continue
		}
		if strings.EqualFold(e.name(name), want) {
			return e
		}
		name = nil
	}
	t.Fatalf("entry %q not found", want)
	return nil
}

// rawEntry is one on-disk 32-byte directory entry.
type rawEntry []byte

func (e rawEntry) cluster() uint32 {
	return uint32(binary.LittleEndian.Uint16(e[20:22]))<<16 | uint32(binary.LittleEndian.Uint16(e[26:28]))
}
func (e rawEntry) fileSize() uint32     { return binary.LittleEndian.Uint32(e[28:32]) }
func (e rawEntry) isLongNamePart() bool { return e[11] == 0x0f }

// longNameChars returns the UTF-16 name characters of one LFN entry, without the terminator or padding.
func (e rawEntry) longNameChars() []uint16 {
	var c []uint16
	for _, p := range []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30} {
		if v := binary.LittleEndian.Uint16(e[p : p+2]); v != 0 && v != 0xffff {
			c = append(c, v)
		}
	}
	return c
}

// name prefers the preceding long name and falls back to the 8.3 short name.
func (e rawEntry) name(long longName) string {
	if name := long.String(); name != "" {
		return name
	}
	return e.shortName()
}

func (e rawEntry) shortName() string {
	base := string(bytes.TrimRight(e[:8], " "))
	ext := string(bytes.TrimRight(e[8:11], " "))
	if ext != "" {
		return base + "." + ext
	}
	return base
}

// longName collects the LFN entries before a short entry, first part first.
type longName [][]uint16

func (n longName) String() string {
	var u []uint16
	for _, part := range n {
		u = append(u, part...)
	}
	return string(utf16.Decode(u))
}

// recordingDevice logs each WriteAt that reaches a memoryDevice.
type recordingDevice struct {
	memoryDevice
	writes []int
}

func (r *recordingDevice) WriteAt(p []byte, off int64) (int, error) {
	r.writes = append(r.writes, len(p))
	return r.memoryDevice.WriteAt(p, off)
}

func newDirtyBuilder(t *testing.T) (*recordingDevice, *Builder) {
	t.Helper()
	d := &recordingDevice{memoryDevice: memoryDevice{b: bytes.Repeat([]byte{0xff}, 64<<20)}}
	b, err := NewBuilder(context.Background(), d, uint64(len(d.b)), "TEST")
	if err != nil {
		t.Fatal(err)
	}
	d.writes = nil
	return d, b
}

// assertWrites checks the WriteAt sizes recorded since the last check and clears them.
func assertWrites(t *testing.T, d *recordingDevice, want ...int) {
	t.Helper()
	if !slices.Equal(d.writes, want) {
		t.Fatalf("writes=%v, want %v", d.writes, want)
	}
	d.writes = nil
}

func TestBuilderWritesContiguousClustersInOneRequest(t *testing.T) {
	d, b := newDirtyBuilder(t)
	cs := b.clusterSize()
	f, err := b.Create("aligned.bin")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, f, bytes.Repeat([]byte{1}, 64*cs))
	assertWrites(t, d, 64*cs)
	mustWrite(t, f, bytes.Repeat([]byte{2}, 3*cs+5))
	assertWrites(t, d, 3*cs+5, cs-5) // data, then the zeroed tail of its last cluster
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBuilderZeroesUnwrittenTailOnDirtyMedia(t *testing.T) {
	d, b := newDirtyBuilder(t)
	cs := b.clusterSize()
	var want []byte
	f, err := b.Create("odd.bin")
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range []int{cs/2 + 7, cs + 3, 11} {
		chunk := bytes.Repeat([]byte{byte(i + 1)}, n)
		mustWrite(t, f, chunk)
		want = append(want, chunk...)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = b.Sync(); err != nil {
		t.Fatal(err)
	}
	image := testImage(d.b)
	e := image.root(t).find(t, "odd.bin")
	chain := image.readChain(t, e.cluster())
	image.assertFileContents(t, e, want)
	if tail := chain[len(want):]; !bytes.Equal(tail, make([]byte, len(tail))) {
		t.Fatal("stale media data left in the last cluster")
	}
}

func TestBuilderSplitsRunsAroundUsedClusters(t *testing.T) {
	d, b := newDirtyBuilder(t)
	cs := b.clusterSize()
	used := b.next + 2 // a cluster another chain already owns
	b.fat[used] = endOfChain
	b.free--
	want := make([]byte, 5*cs)
	for i := range want {
		want[i] = byte(i / cs)
	}
	writeFile(t, b, "split.bin", want)
	assertWrites(t, d, 2*cs, 3*cs) // runs of 2 and 3 clusters on either side of used
	if err := b.Sync(); err != nil {
		t.Fatal(err)
	}
	image := testImage(d.b)
	e := image.root(t).find(t, "split.bin")
	image.assertFileContents(t, e, want)
	for c := e.cluster(); !isChainEnd(c); c = b.fat[c] {
		if c == used {
			t.Fatal("file chain took a used cluster")
		}
	}
}
