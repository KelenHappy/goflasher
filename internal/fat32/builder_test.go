package fat32

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
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
	if err = b.MkdirAll("EFI/啟動工具"); err != nil {
		t.Fatal(err)
	}
	f, err := b.Create("EFI/啟動工具/Very Long Boot Filename.bin")
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte("cluster-chain-data"), b.clusterSize()/4)
	if n, e := f.Write(want); e != nil || n != len(want) {
		t.Fatalf("Write=%d, %v", n, e)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	empty, err := b.Create("EFI/empty.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err = empty.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Create("efi/EMPTY.TXT"); !errors.Is(err, ErrExist) {
		t.Fatalf("collision error=%v", err)
	}
	if err = b.Sync(); err != nil {
		t.Fatal(err)
	}
	if d.synced < 2 {
		t.Fatalf("sync count=%d", d.synced)
	} // format and final sync

	boot := d.b[:512]
	fatOff := int(binary.LittleEndian.Uint16(boot[14:16])) * 512
	fatBytes := int(binary.LittleEndian.Uint32(boot[36:40])) * 512
	if !bytes.Equal(d.b[fatOff:fatOff+fatBytes], d.b[fatOff+fatBytes:fatOff+2*fatBytes]) {
		t.Fatal("FAT mirrors differ")
	}
	root := readChain(t, d.b, boot, 2)
	efi := findEntry(t, root, "EFI")
	efiData := readChain(t, d.b, boot, entryCluster(efi))
	if string(efiData[:1]) != "." || string(efiData[32:34]) != ".." {
		t.Fatal("missing dot entries")
	}
	longDir := findEntry(t, efiData, "啟動工具")
	longData := readChain(t, d.b, boot, entryCluster(longDir))
	file := findEntry(t, longData, "Very Long Boot Filename.bin")
	if got := binary.LittleEndian.Uint32(file[28:32]); got != uint32(len(want)) {
		t.Fatalf("size=%d", got)
	}
	if got := readChain(t, d.b, boot, entryCluster(file))[:len(want)]; !bytes.Equal(got, want) {
		t.Fatal("cross-cluster contents differ")
	}
	emptyEntry := findEntry(t, efiData, "empty.txt")
	if entryCluster(emptyEntry) != 0 || binary.LittleEndian.Uint32(emptyEntry[28:32]) != 0 {
		t.Fatal("empty file allocated a cluster")
	}
	info := d.b[512:1024]
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
	e := findEntry(t, readChain(t, d.b, d.b[:512], 2), "partial.bin")
	if binary.LittleEndian.Uint32(e[28:32]) != uint32(n) {
		t.Fatal("partial size not finalized")
	}
}

func readChain(t *testing.T, image, boot []byte, cluster uint32) []byte {
	t.Helper()
	spc := uint32(boot[13])
	reserved := uint32(binary.LittleEndian.Uint16(boot[14:16]))
	fs := binary.LittleEndian.Uint32(boot[36:40])
	fat := image[reserved*512 : (reserved+fs)*512]
	var out []byte
	seen := map[uint32]bool{}
	for cluster >= 2 && cluster < 0x0ffffff8 {
		if seen[cluster] {
			t.Fatal("FAT loop")
		}
		seen[cluster] = true
		off := (reserved + 2*fs + (cluster-2)*spc) * 512
		out = append(out, image[off:off+spc*512]...)
		cluster = binary.LittleEndian.Uint32(fat[cluster*4:cluster*4+4]) & 0x0fffffff
	}
	return out
}
func entryCluster(e []byte) uint32 {
	return uint32(binary.LittleEndian.Uint16(e[20:22]))<<16 | uint32(binary.LittleEndian.Uint16(e[26:28]))
}
func findEntry(t *testing.T, dir []byte, want string) []byte {
	t.Helper()
	var chunks [][]uint16
	for off := 0; off+32 <= len(dir) && dir[off] != 0; off += 32 {
		e := dir[off : off+32]
		if e[11] == 0x0f {
			var c []uint16
			for _, p := range []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30} {
				v := binary.LittleEndian.Uint16(e[p : p+2])
				if v != 0 && v != 0xffff {
					c = append(c, v)
				}
			}
			chunks = append([][]uint16{c}, chunks...)
			continue
		}
		name := stringsFromLFN(chunks)
		if name == "" {
			name = stringsTrim83(e[:11])
		}
		if stringsEqualFold(name, want) {
			return e
		}
		chunks = nil
	}
	t.Fatalf("entry %q not found", want)
	return nil
}
func stringsFromLFN(c [][]uint16) string {
	var u []uint16
	for _, v := range c {
		u = append(u, v...)
	}
	return string(utf16.Decode(u))
}
func stringsTrim83(a []byte) string {
	base := string(bytes.TrimRight(a[:8], " "))
	ext := string(bytes.TrimRight(a[8:11], " "))
	if ext != "" {
		return base + "." + ext
	}
	return base
}
func stringsEqualFold(a, b string) bool { return strings.EqualFold(a, b) }
