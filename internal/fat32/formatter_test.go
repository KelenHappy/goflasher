package fat32

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"
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
