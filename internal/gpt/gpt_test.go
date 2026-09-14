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
	primary, backup := b[512:1024], b[(total-1)*512:]
	t.Run("mbr", func(t *testing.T) { checkProtectiveMBR(t, b[:512]) })
	t.Run("primary", func(t *testing.T) { checkHeader(t, primary, 1, total-1) })
	t.Run("backup", func(t *testing.T) { checkHeader(t, backup, total-1, 1) })
	t.Run("entries", func(t *testing.T) { checkEntryArrays(t, b, l, primary, backup) })
}

func checkProtectiveMBR(t *testing.T, mbr []byte) {
	t.Helper()
	if mbr[446] != 0 || mbr[450] != 0xee || binary.LittleEndian.Uint32(mbr[454:458]) != 1 || mbr[510] != 0x55 || mbr[511] != 0xaa {
		t.Fatalf("protective MBR=%x", mbr[446:462])
	}
	assertZero(t, mbr[:446], "legacy bootstrap code present")
}

func checkEntryArrays(t *testing.T, b []byte, l *Layout, primary, backup []byte) {
	t.Helper()
	const arrayLen = uint64(EntryCount * EntrySize)
	pa := b[l.PrimaryEntriesLBA*512:][:arrayLen]
	ba := b[l.BackupEntriesLBA*512:][:arrayLen]
	if !bytes.Equal(pa, ba) {
		t.Fatal("entry arrays differ")
	}
	if crc := crc32.ChecksumIEEE(pa); binary.LittleEndian.Uint32(primary[88:92]) != crc || binary.LittleEndian.Uint32(backup[88:92]) != crc {
		t.Fatal("entry CRC mismatch")
	}
	if !bytes.Equal(pa[:16], EFITypeGUID.MarshalBinary()) {
		t.Fatalf("partition type=%x", pa[:16])
	}
	assertZero(t, pa[EntrySize:], "unused entry nonzero")
}

func assertZero(t *testing.T, b []byte, msg string) {
	t.Helper()
	if bytes.ContainsFunc(b, func(r rune) bool { return r != 0 }) {
		t.Fatal(msg)
	}
}

func checkHeader(t *testing.T, h []byte, current, alternate uint64) {
	t.Helper()
	if string(h[:8]) != "EFI PART" {
		t.Fatalf("signature=%q", h[:8])
	}
	fields := []struct {
		name      string
		got, want uint64
	}{
		{"current", binary.LittleEndian.Uint64(h[24:32]), current},
		{"alternate", binary.LittleEndian.Uint64(h[32:40]), alternate},
		{"entry count", uint64(binary.LittleEndian.Uint32(h[80:84])), uint64(EntryCount)},
		{"entry size", uint64(binary.LittleEndian.Uint32(h[84:88])), uint64(EntrySize)},
	}
	for _, f := range fields {
		if f.got != f.want {
			t.Fatalf("header %s=%d want %d", f.name, f.got, f.want)
		}
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
	t.Run("rejects", func(t *testing.T) {
		for _, x := range []struct {
			off int64
			n   int
		}{{-1, 1}, {0, 2048*512 + 1}, {2048 * 512, 1}, {math.MaxInt64, 1}} {
			assertWriteRejected(t, w, x.off, x.n)
		}
		if s.calls != 0 {
			t.Fatalf("calls=%d", s.calls)
		}
	})
	t.Run("translates", func(t *testing.T) {
		if n, err := w.WriteAt([]byte("ok"), 7); err != nil || n != 2 {
			t.Fatalf("valid=(%d,%v)", n, err)
		}
		if s.off != 2048*512+7 {
			t.Fatalf("offset=%d", s.off)
		}
	})
}

func assertWriteRejected(t *testing.T, w io.WriterAt, off int64, n int) {
	t.Helper()
	if got, err := w.WriteAt(make([]byte, n), off); err == nil || got != 0 {
		t.Fatalf("WriteAt(len=%d, off=%d)=(%d,%v) want rejection", n, off, got, err)
	}
}

type sliceWriterAt []byte

func (s sliceWriterAt) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(s)) {
		return 0, io.ErrShortWrite
	}
	return copy(s[off:], p), nil
}
