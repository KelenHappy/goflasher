//go:build linux

package linux

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltInFAT32FormatterCreatesFilesystemWithoutExternalTools(t *testing.T) {
	const size = uint64(64 << 20)
	file := newDirtyDeviceFile(t, size)
	var formatProgress strings.Builder
	requireNoError(t, makeFAT32(file, size, "GOFLASHER", &formatProgress))
	if got, want := formatProgress.String(), "PROGRESS 10 100\nPROGRESS 15 100\nPROGRESS 25 100\nPROGRESS 80 100\nPROGRESS 90 100\nPROGRESS 100 100\n"; got != want {
		t.Fatalf("format progress = %q, want %q", got, want)
	}

	boot := readAt(t, file, 512, 0)
	assertFAT32BootSector(t, boot)
	fatSectors := binary.LittleEndian.Uint32(boot[36:40])
	rootOffset := int64((32 + 2*uint64(fatSectors)) * 512)
	root := readAt(t, file, 32, rootOffset)
	assertRootVolumeLabel(t, root)
	assertZeroedRegion(t, file, zeroedRegion{size: 4 * 512, offset: 2 * 512, description: "primary"})
	assertZeroedRegion(t, file, zeroedRegion{size: 33 * 512, offset: int64(size - 33*512), description: "backup"})
}

func newDirtyDeviceFile(t *testing.T, size uint64) *os.File {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(t.TempDir(), "device"), os.O_CREATE|os.O_RDWR, 0600)
	requireNoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	requireNoError(t, file.Truncate(int64(size)))
	_, err = file.WriteAt(bytes.Repeat([]byte{0xff}, 33*512), int64(size-33*512))
	requireNoError(t, err)
	_, err = file.WriteAt(bytes.Repeat([]byte{0xff}, 32*512), 0)
	requireNoError(t, err)
	return file
}

func readAt(t *testing.T, file *os.File, size int, offset int64) []byte {
	t.Helper()
	data := make([]byte, size)
	_, err := file.ReadAt(data, offset)
	requireNoError(t, err)
	return data
}

func assertFAT32BootSector(t *testing.T, boot []byte) {
	t.Helper()
	if got := string(boot[82:90]); got != "FAT32   " {
		t.Fatalf("filesystem type = %q, want FAT32", got)
	}
	if got := string(boot[71:82]); got != "GOFLASHER  " {
		t.Fatalf("volume label = %q, want GOFLASHER", got)
	}
	if boot[510] != 0x55 {
		t.Fatalf("first boot signature byte = %x, want 55", boot[510])
	}
	if boot[511] != 0xaa {
		t.Fatalf("boot signature = %x, want 55aa", boot[510:512])
	}
}

func assertRootVolumeLabel(t *testing.T, root []byte) {
	t.Helper()
	if got := string(root[:11]); got != "GOFLASHER  " {
		t.Fatalf("root volume label = %q, want GOFLASHER", got)
	}
	if root[11] != 0x08 {
		t.Fatalf("root volume label attribute = %x, want 08", root[11])
	}
}

type zeroedRegion struct {
	size        int
	offset      int64
	description string
}

func assertZeroedRegion(t *testing.T, file *os.File, region zeroedRegion) {
	t.Helper()
	data := readAt(t, file, region.size, region.offset)
	if !bytes.Equal(data, make([]byte, region.size)) {
		t.Fatalf("stale %s partition metadata was not cleared", region.description)
	}
}
