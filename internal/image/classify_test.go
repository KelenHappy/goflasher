package image

import (
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
