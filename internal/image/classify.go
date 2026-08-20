package image

import (
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
func Classify(info Info) (Kind, error) {
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
		return classifyDecoded(info)
	}
	r, size, lease, err := info.RetainedReaderAt()
	if err != nil {
		return UnknownImage, err
	}
	return classifyReader(r, size, lease)
}
func classifyDecoded(info Info) (Kind, error) {
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
	if _, err = io.Copy(f, s); err != nil {
		return UnknownImage, err
	}
	st, err := f.Stat()
	if err != nil {
		return UnknownImage, err
	}
	return classifyReader(f, st.Size(), borrowedSource{f})
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
	if hasHybridMBR(r) {
		return LinuxHybridISO, nil
	}
	return UnknownImage, fmt.Errorf("%w: unsupported ISO content", ErrUnsafeClassification)
}
func hasHybridMBR(r io.ReaderAt) bool {
	b := make([]byte, 512)
	if _, err := r.ReadAt(b, 0); err != nil || b[510] != 0x55 || b[511] != 0xaa {
		return false
	}
	for i := 446; i < 510; i += 16 {
		if b[i+4] != 0 && binary.LittleEndian.Uint32(b[i+12:i+16]) != 0 {
			return true
		}
	}
	return false
}
