package image

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	installeriso "github.com/goflasher/goflasher/internal/installer/iso"
)

type Kind string

const (
	UnknownImage        Kind = "unknown"
	RawDiskImage        Kind = "raw-disk-image"
	LinuxHybridISO      Kind = "linux-hybrid-iso"
	WindowsInstallerISO Kind = "windows-installer-iso"
)

var ErrUnsafeClassification = errors.New("image cannot be safely classified")
var requiredWindowsPaths = [][]string{{"sources/boot.wim"}, {"sources/install.wim", "sources/install.esd"}, {"bootmgr"}, {"efi/boot/bootx64.efi"}}

// Classify uses the retained descriptor. Compressed ISO streams are decoded to
// an anonymous, retained temporary descriptor and classified again; the raw
// writer still consumes the original compressed stream.
func Classify(info Info) (Kind, error) { return ClassifyContext(context.Background(), info) }

func ClassifyContext(ctx context.Context, info Info) (Kind, error) {
	if err := ctx.Err(); err != nil {
		return UnknownImage, err
	}
	if info.Format == FormatIMG || info.Format == FormatRAW {
		return RawDiskImage, nil
	}
	if info.Format != FormatISO {
		return UnknownImage, fmt.Errorf("%w: unsupported container", ErrUnsafeClassification)
	}
	if err := info.ValidateSource(); err != nil {
		return UnknownImage, err
	}
	if info.Compression != CompressionNone {
		return classifyDecoded(ctx, info)
	}
	r, size, lease, err := info.RetainedReaderAt()
	if err != nil {
		return UnknownImage, err
	}
	return classifyReader(r, size, lease)
}
func classifyDecoded(ctx context.Context, info Info) (Kind, error) {
	s, err := Open(info)
	if err != nil {
		return UnknownImage, err
	}
	defer s.Close()
	f, err := os.CreateTemp("", "goflasher-classify-*.iso")
	if err != nil {
		return UnknownImage, err
	}
	name := f.Name()
	_ = os.Remove(name)
	defer f.Close()
	if _, err = io.Copy(f, contextReader{ctx: ctx, reader: s}); err != nil {
		return UnknownImage, err
	}
	st, err := f.Stat()
	if err != nil {
		return UnknownImage, err
	}
	return classifyReader(f, st.Size(), borrowedSource{f})
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err == nil {
		err = r.ctx.Err()
	}
	return n, err
}
func classifyReader(r io.ReaderAt, size int64, lease io.Closer) (Kind, error) {
	fs, err := installeriso.New(r, size, lease)
	if err != nil {
		return UnknownImage, fmt.Errorf("%w: %v", ErrUnsafeClassification, err)
	}
	defer fs.Close()
	paths := map[string]bool{}
	for _, e := range fs.Manifest().Entries {
		paths[strings.ToLower(e.Path)] = true
	}
	win := true
	for _, alts := range requiredWindowsPaths {
		found := false
		for _, p := range alts {
			found = found || paths[p]
		}
		win = win && found
	}
	if win {
		return WindowsInstallerISO, nil
	}
	if hasWindowsInstallerSignals(paths) {
		return UnknownImage, fmt.Errorf("%w: incomplete Windows installer manifest", ErrUnsafeClassification)
	}
	if hasLinuxHybridSignals(paths) && hasValidHybridMBR(r, uint64(size)) {
		return LinuxHybridISO, nil
	}
	return UnknownImage, fmt.Errorf("%w: unsupported ISO content", ErrUnsafeClassification)
}
func hasWindowsInstallerSignals(paths map[string]bool) bool {
	for p := range paths {
		if p == "bootmgr" || strings.HasPrefix(p, "sources/boot.wim") || strings.HasPrefix(p, "sources/install.") || strings.HasPrefix(p, "efi/microsoft/boot/") || (strings.HasPrefix(p, "efi/boot/boot") && strings.HasSuffix(p, ".efi")) {
			return true
		}
	}
	return false
}

func hasLinuxHybridSignals(paths map[string]bool) bool {
	for _, p := range []string{"isolinux/isolinux.bin", "boot/grub/grub.cfg", "boot/grub/i386-pc/eltorito.img", ".disk/info"} {
		if paths[p] {
			return true
		}
	}
	return false
}

func hasValidHybridMBR(r io.ReaderAt, sourceSize uint64) bool {
	b := make([]byte, 512)
	if _, err := r.ReadAt(b, 0); err != nil || b[510] != 0x55 || b[511] != 0xaa {
		return false
	}
	totalSectors := sourceSize / 512
	if sourceSize%512 != 0 {
		totalSectors++
	}
	var previousEnd uint64
	for i := 446; i < 510; i += 16 {
		boot, partitionType := b[i], b[i+4]
		start := uint64(binary.LittleEndian.Uint32(b[i+8 : i+12]))
		count := uint64(binary.LittleEndian.Uint32(b[i+12 : i+16]))
		if partitionType == 0 && count == 0 {
			continue
		}
		if (boot != 0 && boot != 0x80) || partitionType == 0 || start == 0 || count == 0 || start >= totalSectors || count > totalSectors-start {
			return false
		}
		if previousEnd != 0 && start < previousEnd {
			return false
		}
		previousEnd = start + count
	}
	// A hybrid partition describes the optical image payload, rather than an
	// arbitrary in-bounds range planted in the ISO system area.
	return previousEnd == totalSectors
}
