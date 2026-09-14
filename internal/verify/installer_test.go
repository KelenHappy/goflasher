package verify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/goflasher/goflasher/internal/fat32"
	"github.com/goflasher/goflasher/internal/gpt"
	"github.com/goflasher/goflasher/internal/installer"
)

type installerDisk []byte

func (d installerDisk) WriteAt(p []byte, off int64) (int, error) {
	if !d.fits(len(p), off) {
		return 0, fmt.Errorf("bounds")
	}
	return copy(d[int(off):], p), nil
}

func (d installerDisk) fits(n int, off int64) bool {
	return off >= 0 && int(off) <= len(d) && n <= len(d)-int(off)
}
func (installerDisk) Sync() error { return nil }

func buildInstallerDisk(t *testing.T, split bool) (installerDisk, []installer.VerificationEntry) {
	t.Helper()
	d, b := newInstallerBuilder(t)
	var manifest []installer.VerificationEntry
	for name, data := range installerFiles(split) {
		manifest = append(manifest, writeInstallerFile(t, b, name, data))
	}
	if err := b.Sync(); err != nil {
		t.Fatal(err)
	}
	return d, manifest
}

// newInstallerBuilder writes a GPT layout to a fresh disk and opens a FAT32
// builder over its single partition.
func newInstallerBuilder(t *testing.T) (installerDisk, *fat32.Builder) {
	t.Helper()
	const diskSize = 80 << 20
	d := make(installerDisk, diskSize)
	random := append(bytes.Repeat([]byte{7}, 16), bytes.Repeat([]byte{9}, 16)...)
	layout, err := gpt.Build(diskSize/512, 512, bytes.NewReader(random))
	if err != nil {
		t.Fatal(err)
	}
	if err = layout.WriteTo(d); err != nil {
		t.Fatal(err)
	}
	partition, err := gpt.NewPartitionWriterAt(d, layout.PartitionStartLBA, layout.PartitionEndLBA, 512)
	if err != nil {
		t.Fatal(err)
	}
	size := (layout.PartitionEndLBA - layout.PartitionStartLBA + 1) * 512
	b, err := fat32.NewBuilder(context.Background(), partition, size, "GOFLASHER")
	if err != nil {
		t.Fatal(err)
	}
	return d, b
}

func installerFiles(split bool) map[string][]byte {
	files := map[string][]byte{"efi/boot/bootx64.efi": bytes.Repeat([]byte("EFI-LOADER!"), 73), "sources/boot.wim": bytes.Repeat([]byte("BOOT-WIM!"), 97)}
	if split {
		files["sources/install.swm"] = bytes.Repeat([]byte("SWM-ONE!"), 89)
		files["sources/install2.swm"] = bytes.Repeat([]byte("SWM-TWO!"), 67)
	}
	return files
}

// writeInstallerFile stores data at name and returns its manifest entry.
func writeInstallerFile(t *testing.T, b *fat32.Builder, name string, data []byte) installer.VerificationEntry {
	t.Helper()
	if err := b.MkdirAll(directory(name)); err != nil {
		t.Fatal(err)
	}
	f, err := b.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return installer.VerificationEntry{Path: name, Size: uint64(len(data)), SHA256: fmt.Sprintf("%x", sum)}
}
func directory(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[:i]
		}
	}
	return ""
}

func TestVerifyInstallerReadsRawGPTFATAndManifest(t *testing.T) {
	d, manifest := buildInstallerDisk(t, true)
	result, err := VerifyInstaller(context.Background(), RawTarget{Reader: bytes.NewReader(d), Size: uint64(len(d))}, manifest, InstallerOptions{SplitWIMPolicySize: 3800 << 20, RequireSplitWIM: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.ManifestSHA256 == "" {
		t.Fatalf("manifest hash missing: result=%+v", result)
	}
	if result.FilesVerified != 4 || result.WIMParts != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestVerifyInstallerRejectsRawMetadataAndContentCorruption(t *testing.T) {
	d, manifest := buildInstallerDisk(t, false)
	bootOff := uint64(binary.LittleEndian.Uint64(d[512+72 : 512+80])) // keep compiler exercising raw offsets
	_ = bootOff
	primaryEntries := uint64(binary.LittleEndian.Uint64(d[512+72:512+80])) * 512
	partitionStart := binary.LittleEndian.Uint64(d[primaryEntries+32 : primaryEntries+40])
	boot := d[partitionStart*512:]
	fat1 := partitionStart*512 + uint64(binary.LittleEndian.Uint16(boot[14:16]))*512
	fatBytes := uint64(binary.LittleEndian.Uint32(boot[36:40])) * 512
	content := bytes.Index(d, bytes.Repeat([]byte("EFI-LOADER!"), 10))
	if content < 0 {
		t.Fatal("content not found")
	}
	tests := []struct {
		name   string
		offset uint64
	}{{"protective MBR", 510}, {"primary header CRC", 512 + 16}, {"partition array CRC", primaryEntries}, {"FAT mirror", fat1 + fatBytes}, {"file hash", uint64(content)}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d[tt.offset] ^= 0xff
			_, err := VerifyInstaller(context.Background(), RawTarget{Reader: bytes.NewReader(d), Size: uint64(len(d))}, manifest, InstallerOptions{SplitWIMPolicySize: 3800 << 20})
			d[tt.offset] ^= 0xff
			if err == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
}
