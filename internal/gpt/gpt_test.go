package gpt

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
	"math"
	"testing"
)

func TestGUIDOnDiskKnownVector(t *testing.T) {
	g := GUID{0, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	want := []byte{0x33, 0x22, 0x11, 0, 0x55, 0x44, 0x77, 0x66, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	if !bytes.Equal(g.MarshalBinary(), want) {
		t.Fatalf("GUID=%x want %x", g.MarshalBinary(), want)
	}
}

func TestBuildAndWriteGPT(t *testing.T) {
	const total = uint64(8192)
	l, err := Build(total, 512, bytes.NewReader([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}))
	if err != nil {
		t.Fatal(err)
	}
	if l.PartitionStartLBA != 2048 || l.PartitionEndLBA != l.LastUsableLBA {
		t.Fatalf("partition=%d..%d usable=%d..%d", l.PartitionStartLBA, l.PartitionEndLBA, l.FirstUsableLBA, l.LastUsableLBA)
	}
	b := make([]byte, total*512)
	if err := l.WriteTo(sliceWriterAt(b)); err != nil {
		t.Fatal(err)
	}
	mbr := b[:512]
	if mbr[446] != 0 || mbr[450] != 0xee || binary.LittleEndian.Uint32(mbr[454:458]) != 1 || mbr[510] != 0x55 || mbr[511] != 0xaa {
		t.Fatalf("protective MBR=%x", mbr[446:462])
	}
	for _, v := range mbr[:446] {
		if v != 0 {
			t.Fatal("legacy bootstrap code present")
		}
	}
	primary := b[512:1024]
	backup := b[(total-1)*512 : total*512]
	checkHeader(t, primary, 1, total-1)
	checkHeader(t, backup, total-1, 1)
	pa := b[l.PrimaryEntriesLBA*512 : l.PrimaryEntriesLBA*512+uint64(EntryCount*EntrySize)]
	ba := b[l.BackupEntriesLBA*512 : l.BackupEntriesLBA*512+uint64(EntryCount*EntrySize)]
	if !bytes.Equal(pa, ba) {
		t.Fatal("entry arrays differ")
	}
	if binary.LittleEndian.Uint32(primary[88:92]) != crc32.ChecksumIEEE(pa) || binary.LittleEndian.Uint32(backup[88:92]) != crc32.ChecksumIEEE(ba) {
		t.Fatal("entry CRC mismatch")
	}
	if !bytes.Equal(pa[:16], EFITypeGUID.MarshalBinary()) {
		t.Fatalf("partition type=%x", pa[:16])
	}
	for _, v := range pa[EntrySize:] {
		if v != 0 {
			t.Fatal("unused entry nonzero")
		}
	}
}
func checkHeader(t *testing.T, h []byte, current, alternate uint64) {
	t.Helper()
	if string(h[:8]) != "EFI PART" || binary.LittleEndian.Uint64(h[24:32]) != current || binary.LittleEndian.Uint64(h[32:40]) != alternate || binary.LittleEndian.Uint32(h[80:84]) != EntryCount || binary.LittleEndian.Uint32(h[84:88]) != EntrySize {
		t.Fatal("header fields invalid")
	}
	n := binary.LittleEndian.Uint32(h[12:16])
	c := append([]byte(nil), h[:n]...)
	want := binary.LittleEndian.Uint32(c[16:20])
	binary.LittleEndian.PutUint32(c[16:20], 0)
	if crc32.ChecksumIEEE(c) != want {
		t.Fatal("header CRC mismatch")
	}
}

func TestLayoutUsesLogicalSectorLBAs(t *testing.T) {
	l, err := Build(4096, 4096, bytes.NewReader(append(make([]byte, 16), bytes.Repeat([]byte{1}, 16)...)))
	if err != nil {
		t.Fatal(err)
	}
	if l.PartitionStartLBA != 256 {
		t.Fatalf("start=%d", l.PartitionStartLBA)
	}
	if l.FirstUsableLBA != 6 || l.BackupEntriesLBA != 4091 {
		t.Fatalf("metadata=%d,%d", l.FirstUsableLBA, l.BackupEntriesLBA)
	}
}
func TestBuildRejectsBoundsAndOverflow(t *testing.T) {
	for _, x := range []struct{ lba, sector uint64 }{{1, 512}, {math.MaxUint64, 4096}, {4096, 1000}} {
		if _, err := Build(x.lba, x.sector, bytes.NewReader(make([]byte, 32))); err == nil {
			t.Fatalf("Build(%d,%d) succeeded", x.lba, x.sector)
		}
	}
}

type spyWriterAt struct {
	calls int
	off   int64
}

func (s *spyWriterAt) WriteAt(p []byte, off int64) (int, error) {
	s.calls++
	s.off = off
	return len(p), nil
}
func TestPartitionWriterAtIsBoundedAndAtomicOnRejection(t *testing.T) {
	s := &spyWriterAt{}
	w, err := NewPartitionWriterAt(s, 2048, 4095, 512)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		off int64
		n   int
	}{{-1, 1}, {0, 2048*512 + 1}, {2048 * 512, 1}} {
		if n, err := w.WriteAt(make([]byte, x.n), x.off); err == nil || n != 0 {
			t.Fatalf("write=(%d,%v)", n, err)
		}
	}
	if s.calls != 0 {
		t.Fatalf("calls=%d", s.calls)
	}
	if n, err := w.WriteAt([]byte("ok"), 7); err != nil || n != 2 {
		t.Fatalf("valid=(%d,%v)", n, err)
	}
	if s.off != 2048*512+7 {
		t.Fatalf("offset=%d", s.off)
	}
}

type sliceWriterAt []byte

func (s sliceWriterAt) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(s)) {
		return 0, io.ErrShortWrite
	}
	return copy(s[off:], p), nil
}
