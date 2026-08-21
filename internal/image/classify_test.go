package image

import (
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyWindowsInstallerByManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "definitely-not-windows.iso")
	if err := os.WriteFile(path, installerISO(true, false), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(Info{Path: path, Format: FormatISO, Compression: CompressionNone})
	if err != nil {
		t.Fatal(err)
	}
	defer info.CloseSource()
	got, err := Classify(info)
	if err != nil || got != WindowsInstallerISO {
		t.Fatalf("Classify() = %q, %v", got, err)
	}
}

func TestCanonicalPreSplitWindowsInstallSet(t *testing.T) {
	base := map[string]bool{
		"sources/boot.wim": true, "bootmgr": true,
		"efi/boot/bootx64.efi": true, "sources/install.swm": true,
		"sources/install2.swm": true,
	}
	if !hasCanonicalInstallImage(base) {
		t.Fatal("contiguous pre-split SWM set was not recognized")
	}
	for name, paths := range map[string]map[string]bool{
		"missing first":  {"sources/install2.swm": true},
		"missing middle": {"sources/install.swm": true, "sources/install3.swm": true},
		"malformed":      {"sources/install.swm": true, "sources/install02.swm": true},
	} {
		t.Run(name, func(t *testing.T) {
			if hasCanonicalInstallImage(paths) {
				t.Fatal("invalid SWM set was recognized")
			}
		})
	}
}

func TestClassifyISOFailClosedWithoutCompleteManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "windows-installer.iso")
	if err := os.WriteFile(path, installerISO(false, false), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(Info{Path: path, Format: FormatISO, Compression: CompressionNone})
	if err != nil {
		t.Fatal(err)
	}
	defer info.CloseSource()
	if got, err := Classify(info); got != UnknownImage || !errors.Is(err, ErrUnsafeClassification) {
		t.Fatalf("Classify() = %q, %v", got, err)
	}
}

func TestClassifyLinuxHybridISOByFilesystemAndPartitionTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "distribution.iso")
	if err := os.WriteFile(path, installerISO(false, true), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(Info{Path: path, Format: FormatISO, Compression: CompressionNone})
	if err != nil {
		t.Fatal(err)
	}
	defer info.CloseSource()
	if got, err := Classify(info); err != nil || got != LinuxHybridISO {
		t.Fatalf("Classify() = %q, %v", got, err)
	}
}

func TestClassifyLinuxHybridISOWithWholeImageAndEmbeddedPartitions(t *testing.T) {
	b := installerISO(false, true)
	totalSectors := uint32(len(b) / 512)
	// A type-zero entry covers the complete image, while an EFI partition is
	// embedded inside that range.
	b[446+4] = 0
	binary.LittleEndian.PutUint32(b[446+8:], 0)
	binary.LittleEndian.PutUint32(b[446+12:], totalSectors)
	b[462+4] = 0xef
	binary.LittleEndian.PutUint32(b[462+8:], 4)
	binary.LittleEndian.PutUint32(b[462+12:], 8)

	path := filepath.Join(t.TempDir(), "distribution.iso")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(Info{Path: path, Format: FormatISO, Compression: CompressionNone})
	if err != nil {
		t.Fatal(err)
	}
	defer info.CloseSource()
	if got, err := Classify(info); err != nil || got != LinuxHybridISO {
		t.Fatalf("Classify() = %q, %v", got, err)
	}
}

func TestClassifyDecodedRemovesTemporaryFile(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	path := filepath.Join(t.TempDir(), "distribution.iso.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write(installerISO(false, true)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(Info{Path: path, Format: FormatISO, Compression: CompressionGzip})
	if err != nil {
		t.Fatal(err)
	}
	defer info.CloseSource()
	if got, err := Classify(info); err != nil || got != LinuxHybridISO {
		t.Fatalf("Classify() = %q, %v", got, err)
	}
	entries, err := os.ReadDir(temp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("decoded temporary files remain: %v", entries)
	}
}

func TestHybridClassificationRejectsInvalidPartitionAndIncompleteWindows(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte)
	}{
		{name: "zero start LBA", mutate: func(b []byte) { binary.LittleEndian.PutUint32(b[446+8:], 0) }},
		{name: "out of bounds", mutate: func(b []byte) {
			binary.LittleEndian.PutUint32(b[446+8:], 39)
			binary.LittleEndian.PutUint32(b[446+12:], 2)
		}},
		{name: "incomplete Windows signals", mutate: func(b []byte) {
			root := b[20*2048 : 21*2048]
			off := 0
			for root[off] != 0 {
				off += int(root[off])
			}
			copy(root[off:], isoRecord(35, 1, []byte("BOOTMGR;1"), false))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := installerISO(false, true)
			tt.mutate(b)
			path := filepath.Join(t.TempDir(), "linux.iso")
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			info, err := Inspect(Info{Path: path, Format: FormatISO, Compression: CompressionNone})
			if err != nil {
				t.Fatal(err)
			}
			defer info.CloseSource()
			if got, err := Classify(info); got != UnknownImage || !errors.Is(err, ErrUnsafeClassification) {
				t.Fatalf("Classify() = (%s, %v)", got, err)
			}
		})
	}
}

type countingReader struct{ calls int }

func (r *countingReader) Read(p []byte) (int, error) { r.calls++; return copy(p, "data"), nil }
func TestContextReaderHonorsCancellationBeforeReading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	underlying := &countingReader{}
	if n, err := (contextReader{ctx: ctx, reader: underlying}).Read(make([]byte, 4)); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() = (%d, %v)", n, err)
	}
	if underlying.calls != 0 {
		t.Fatalf("underlying reads = %d", underlying.calls)
	}
}

// installerISO constructs a minimal ISO9660 tree used to exercise the parser,
// without relying on host ISO authoring tools.
func installerISO(complete, hybrid bool) []byte {
	b := make([]byte, 40*2048)
	if hybrid {
		b[446+4] = 0x17
		binary.LittleEndian.PutUint32(b[446+12:], 40)
		b[510], b[511] = 0x55, 0xaa
	}
	pvd := b[16*2048 : 17*2048]
	pvd[0] = 1
	copy(pvd[1:], "CD001")
	pvd[6] = 1
	copy(pvd[156:], isoRecord(20, 2048, []byte{0}, true))
	term := b[17*2048 : 18*2048]
	term[0] = 255
	copy(term[1:], "CD001")
	term[6] = 1
	dirs := map[uint32][][]byte{
		20: {isoRecord(20, 2048, []byte{0}, true), isoRecord(20, 2048, []byte{1}, true), isoRecord(21, 2048, []byte("SOURCES"), true), isoRecord(22, 2048, []byte("EFI"), true), isoRecord(30, 1, []byte("BOOTMGR;1"), false)},
		21: {isoRecord(21, 2048, []byte{0}, true), isoRecord(20, 2048, []byte{1}, true), isoRecord(31, 1, []byte("BOOT.WIM;1"), false)},
		22: {isoRecord(22, 2048, []byte{0}, true), isoRecord(20, 2048, []byte{1}, true), isoRecord(23, 2048, []byte("BOOT"), true)},
		23: {isoRecord(23, 2048, []byte{0}, true), isoRecord(22, 2048, []byte{1}, true), isoRecord(33, 1, []byte("BOOTX64.EFI;1"), false)},
	}
	if hybrid {
		dirs = map[uint32][][]byte{
			20: {isoRecord(20, 2048, []byte{0}, true), isoRecord(20, 2048, []byte{1}, true), isoRecord(24, 2048, []byte(".DISK"), true)},
			24: {isoRecord(24, 2048, []byte{0}, true), isoRecord(20, 2048, []byte{1}, true), isoRecord(34, 1, []byte("INFO;1"), false)},
		}
		binary.LittleEndian.PutUint32(b[446+8:], 1)
		binary.LittleEndian.PutUint32(b[446+12:], 159)
	}
	if complete {
		dirs[21] = append(dirs[21], isoRecord(32, 1, []byte("INSTALL.WIM;1"), false))
	}
	for sector, records := range dirs {
		off := int(sector) * 2048
		for _, rec := range records {
			copy(b[off:], rec)
			off += len(rec)
		}
	}
	return b
}

func isoRecord(extent, size uint32, name []byte, directory bool) []byte {
	n := 33 + len(name)
	if n%2 != 0 {
		n++
	}
	r := make([]byte, n)
	r[0] = byte(n)
	binary.LittleEndian.PutUint32(r[2:], extent)
	binary.BigEndian.PutUint32(r[6:], extent)
	binary.LittleEndian.PutUint32(r[10:], size)
	binary.BigEndian.PutUint32(r[14:], size)
	if directory {
		r[25] = 2
	}
	r[28], r[31] = 1, 1
	r[32] = byte(len(name))
	copy(r[33:], name)
	return r
}
