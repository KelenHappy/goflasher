package fat32

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/goflasher/goflasher/internal/gpt"
)

func TestFormatCreatesFAT32AndReportsProgress(t *testing.T) {
	const size = uint64(64 << 20)
	f := newTempDisk(t, "disk", size)
	var got []uint64
	if err := Format(context.Background(), Request{Device: f, Size: size, Label: "GOFLASHER", Progress: func(p uint64) { got = append(got, p) }}); err != nil {
		t.Fatal(err)
	}
	boot := readDiskRange(t, f, 0, 512)
	if !isFAT32BootSector(boot) {
		t.Fatal("invalid boot sector")
	}
	if binary.LittleEndian.Uint16(boot[11:13]) != 512 {
		t.Fatal("invalid sector size")
	}
	if want := []uint64{10, 15, 25, 80, 90, 100}; !slices.Equal(got, want) {
		t.Fatalf("progress=%v, want %v", got, want)
	}
}

func TestFormatPartitionPreservesGPTMetadata(t *testing.T) {
	const (
		sectorSize = uint64(512)
		totalLBAs  = uint64(135168) // 66 MiB: a 1 MiB gap and a >=64 MiB ESP.
	)
	f := newTempDisk(t, "gpt-disk", totalLBAs*sectorSize)
	l := writeTestGPT(t, f, totalLBAs, sectorSize)
	untouched := []*diskSnapshot{
		{size: l.FirstUsableLBA * sectorSize,
			what: "protective MBR, primary GPT header, or primary entries"},
		{off: l.BackupEntriesLBA * sectorSize, size: (totalLBAs - l.BackupEntriesLBA) * sectorSize,
			what: "backup GPT entries or header"},
	}
	for _, s := range untouched {
		s.capture(t, f)
	}

	formatTestPartition(t, f, l, sectorSize)

	for _, s := range untouched {
		s.requireUnchanged(t, f)
	}
	if !isFAT32BootSector(readDiskRange(t, f, l.PartitionStartLBA*sectorSize, sectorSize)) {
		t.Fatal("ESP does not contain a FAT32 boot sector at its partition-relative offset zero")
	}
}

func TestFormatRejectsInvalidInputs(t *testing.T) {
	f := newTempDisk(t, "disk", 0)
	for _, tt := range []struct {
		size  uint64
		label string
	}{{64 << 20, "bad label"}, {1 << 20, "GOOD"}} {
		if err := Format(context.Background(), Request{Device: f, Size: tt.size, Label: tt.label}); err == nil {
			t.Fatalf("accepted %+v", tt)
		}
	}
}

func TestFormatHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := newTempDisk(t, "disk", 0)
	if err := Format(ctx, Request{Device: f, Size: 64 << 20, Label: "GOOD"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

// newTempDisk creates a temporary disk image of the given size that is closed
// when the test ends.
func newTempDisk(t *testing.T, pattern string, size uint64) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), pattern)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if err = f.Truncate(int64(size)); err != nil {
		t.Fatal(err)
	}
	return f
}

// writeTestGPT writes a deterministic GPT layout to f.
func writeTestGPT(t *testing.T, f *os.File, totalLBAs, sectorSize uint64) *gpt.Layout {
	t.Helper()
	random := append(bytes.Repeat([]byte{0x5a}, 16), bytes.Repeat([]byte{0xa5}, 16)...)
	l, err := gpt.Build(totalLBAs, sectorSize, bytes.NewReader(random))
	if err != nil {
		t.Fatal(err)
	}
	if err = l.WriteTo(f); err != nil {
		t.Fatal(err)
	}
	return l
}

// formatTestPartition formats the ESP described by l through a partition-bounded writer.
func formatTestPartition(t *testing.T, f *os.File, l *gpt.Layout, sectorSize uint64) {
	t.Helper()
	partition, err := gpt.NewPartitionWriterAt(f, l.PartitionStartLBA, l.PartitionEndLBA, sectorSize)
	if err != nil {
		t.Fatal(err)
	}
	partitionSize := (l.PartitionEndLBA - l.PartitionStartLBA + 1) * sectorSize
	if err = FormatPartition(context.Background(), Request{Device: partition, Size: partitionSize, Label: "GOFLASHER"}); err != nil {
		t.Fatal(err)
	}
}

// diskSnapshot is a byte range recorded before formatting, so the test can
// prove the formatter left it untouched. what names it in the failure.
type diskSnapshot struct {
	off, size uint64
	what      string
	before    []byte
}

func (s *diskSnapshot) capture(t *testing.T, f *os.File) {
	t.Helper()
	s.before = readDiskRange(t, f, s.off, s.size)
}

func (s *diskSnapshot) requireUnchanged(t *testing.T, f *os.File) {
	t.Helper()
	if !bytes.Equal(readDiskRange(t, f, s.off, s.size), s.before) {
		t.Fatalf("formatter modified the %s", s.what)
	}
}

func isFAT32BootSector(boot []byte) bool {
	hasSignature := boot[510] == 0x55 && boot[511] == 0xaa
	return hasSignature && string(boot[82:90]) == "FAT32   "
}

func readDiskRange(t *testing.T, f *os.File, off, size uint64) []byte {
	t.Helper()
	b := make([]byte, size)
	if _, err := f.ReadAt(b, int64(off)); err != nil {
		t.Fatal(err)
	}
	return b
}
