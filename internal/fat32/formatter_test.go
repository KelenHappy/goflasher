package fat32

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"github.com/goflasher/goflasher/internal/gpt"
)

func TestFormatCreatesFAT32AndReportsProgress(t *testing.T) {
	const size = uint64(64 << 20)
	f, err := os.CreateTemp(t.TempDir(), "disk")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = f.Truncate(int64(size)); err != nil {
		t.Fatal(err)
	}
	var got []uint64
	if err = Format(context.Background(), f, size, "GOFLASHER", func(p uint64) { got = append(got, p) }); err != nil {
		t.Fatal(err)
	}
	boot := make([]byte, 512)
	if _, err = f.ReadAt(boot, 0); err != nil {
		t.Fatal(err)
	}
	if string(boot[82:90]) != "FAT32   " || boot[510] != 0x55 || boot[511] != 0xaa {
		t.Fatal("invalid boot sector")
	}
	if binary.LittleEndian.Uint16(boot[11:13]) != 512 {
		t.Fatal("invalid sector size")
	}
	want := []uint64{10, 15, 25, 80, 90, 100}
	if len(got) != len(want) {
		t.Fatalf("progress=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("progress=%v", got)
		}
	}
}

func TestFormatPartitionPreservesGPTMetadata(t *testing.T) {
	const (
		sectorSize = uint64(512)
		totalLBAs  = uint64(135168) // 66 MiB: a 1 MiB gap and a >=64 MiB ESP.
	)
	f, err := os.CreateTemp(t.TempDir(), "gpt-disk")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = f.Truncate(int64(totalLBAs * sectorSize)); err != nil {
		t.Fatal(err)
	}
	random := append(bytes.Repeat([]byte{0x5a}, 16), bytes.Repeat([]byte{0xa5}, 16)...)
	l, err := gpt.Build(totalLBAs, sectorSize, bytes.NewReader(random))
	if err != nil {
		t.Fatal(err)
	}
	if err = l.WriteTo(f); err != nil {
		t.Fatal(err)
	}
	primaryBefore := readDiskRange(t, f, 0, l.FirstUsableLBA*sectorSize)
	backupOffset := l.BackupEntriesLBA * sectorSize
	backupBefore := readDiskRange(t, f, backupOffset, (totalLBAs-l.BackupEntriesLBA)*sectorSize)

	partition, err := gpt.NewPartitionWriterAt(f, l.PartitionStartLBA, l.PartitionEndLBA, sectorSize)
	if err != nil {
		t.Fatal(err)
	}
	partitionSize := (l.PartitionEndLBA - l.PartitionStartLBA + 1) * sectorSize
	if err = FormatPartition(context.Background(), partition, partitionSize, "GOFLASHER", nil); err != nil {
		t.Fatal(err)
	}

	if got := readDiskRange(t, f, 0, uint64(len(primaryBefore))); !bytes.Equal(got, primaryBefore) {
		t.Fatal("formatter modified the protective MBR, primary GPT header, or primary entries")
	}
	if got := readDiskRange(t, f, backupOffset, uint64(len(backupBefore))); !bytes.Equal(got, backupBefore) {
		t.Fatal("formatter modified the backup GPT entries or header")
	}
	boot := readDiskRange(t, f, l.PartitionStartLBA*sectorSize, sectorSize)
	if string(boot[82:90]) != "FAT32   " || boot[510] != 0x55 || boot[511] != 0xaa {
		t.Fatal("ESP does not contain a FAT32 boot sector at its partition-relative offset zero")
	}
}

func readDiskRange(t *testing.T, f *os.File, off, size uint64) []byte {
	t.Helper()
	b := make([]byte, size)
	if _, err := f.ReadAt(b, int64(off)); err != nil {
		t.Fatal(err)
	}
	return b
}
func TestFormatRejectsInvalidInputs(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "disk")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, tt := range []struct {
		size  uint64
		label string
	}{{64 << 20, "bad label"}, {1 << 20, "GOOD"}} {
		if err := Format(context.Background(), f, tt.size, tt.label, nil); err == nil {
			t.Fatalf("accepted %+v", tt)
		}
	}
}
func TestFormatHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f, err := os.CreateTemp(t.TempDir(), "disk")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = Format(ctx, f, 64<<20, "GOOD", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
