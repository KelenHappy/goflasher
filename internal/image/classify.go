package image

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
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
var requiredWindowsPaths = [][]string{{"sources/boot.wim"}, {"bootmgr"}, {"efi/boot/bootx64.efi"}}

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
	defer func() {
		_ = f.Close()
		_ = os.Remove(name)
	}()
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
	paths := manifestPaths(fs)
	if isWindowsInstaller(paths) {
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

func manifestPaths(fs *installeriso.Reader) map[string]bool {
	paths := map[string]bool{}
	for _, e := range fs.Manifest().Entries {
		paths[strings.ToLower(e.Path)] = true
	}
	return paths
}

func isWindowsInstaller(paths map[string]bool) bool {
	return hasCanonicalInstallImage(paths) && hasAllRequired(paths, requiredWindowsPaths)
}

// hasAllRequired is satisfied when every alternative group has at least one
// present member.
func hasAllRequired(paths map[string]bool, groups [][]string) bool {
	for _, alts := range groups {
		if !hasAny(paths, alts) {
			return false
		}
	}
	return true
}

func hasAny(paths map[string]bool, candidates []string) bool {
	for _, p := range candidates {
		if paths[p] {
			return true
		}
	}
	return false
}

// hasCanonicalInstallImage recognizes either a single WIM/ESD or an existing
// contiguous install.swm, install2.swm, ... set. Any install*.swm spelling
// outside that convention makes the set ambiguous and therefore unsupported.
func hasCanonicalInstallImage(paths map[string]bool) bool {
	if paths["sources/install.wim"] || paths["sources/install.esd"] {
		return true
	}
	seen := map[int]bool{}
	for name := range paths {
		n, isPart, err := swmPartIndex(name)
		if err != nil {
			return false
		}
		if isPart {
			seen[n] = true
		}
	}
	return len(seen) > 0 && contiguousFrom1(seen)
}

// swmPartIndex reports the 1-based index of a split WIM part. Names that
// look like parts but do not round-trip through the canonical spelling are
// errors rather than non-parts.
func swmPartIndex(name string) (int, bool, error) {
	if name == "sources/install.swm" {
		return 1, true, nil
	}
	if !strings.HasPrefix(name, "sources/install") || !strings.HasSuffix(name, ".swm") {
		return 0, false, nil
	}
	text := strings.TrimSuffix(strings.TrimPrefix(name, "sources/install"), ".swm")
	n, err := strconv.Atoi(text)
	if err != nil || n < 2 {
		return 0, false, fmt.Errorf("non-canonical SWM name %q", name)
	}
	if name != fmt.Sprintf("sources/install%d.swm", n) {
		return 0, false, fmt.Errorf("non-canonical SWM name %q", name)
	}
	return n, true, nil
}

func contiguousFrom1(seen map[int]bool) bool {
	for n := 1; n <= len(seen); n++ {
		if !seen[n] {
			return false
		}
	}
	return true
}

func hasWindowsInstallerSignals(paths map[string]bool) bool {
	for p := range paths {
		if isWindowsInstallerSignal(p) {
			return true
		}
	}
	return false
}

func isWindowsInstallerSignal(p string) bool {
	if p == "bootmgr" {
		return true
	}
	if strings.HasPrefix(p, "sources/boot.wim") || strings.HasPrefix(p, "sources/install.") {
		return true
	}
	if strings.HasPrefix(p, "efi/microsoft/boot/") {
		return true
	}
	return strings.HasPrefix(p, "efi/boot/boot") && strings.HasSuffix(p, ".efi")
}

func hasLinuxHybridSignals(paths map[string]bool) bool {
	return hasAny(paths, []string{"isolinux/isolinux.bin", "boot/grub/grub.cfg", "boot/grub/i386-pc/eltorito.img", ".disk/info"})
}

// mbrPartition is one 16-byte entry of the classic MBR partition table.
type mbrPartition struct {
	boot, kind   byte
	start, count uint64
}

func parseMBRPartition(e []byte) mbrPartition {
	return mbrPartition{
		boot:  e[0],
		kind:  e[4],
		start: uint64(binary.LittleEndian.Uint32(e[8:12])),
		count: uint64(binary.LittleEndian.Uint32(e[12:16])),
	}
}

func (p mbrPartition) empty() bool { return p.kind == 0 && p.count == 0 }

func (p mbrPartition) validBootFlag() bool { return p.boot == 0 || p.boot == 0x80 }

// coversImage matches the Debian- and Ubuntu-style isohybrid entry spanning
// the complete image. It is metadata for the image as a whole (and is
// sometimes type 0), so embedded partitions may overlap it. An exact
// source-sized range is required so an otherwise invalid zero-start entry is
// not mistaken for it.
func (p mbrPartition) coversImage(totalSectors uint64) bool {
	return p.start == 0 && p.count == totalSectors
}

func (p mbrPartition) inBounds(totalSectors uint64) bool {
	if p.kind == 0 || p.start == 0 || p.count == 0 {
		return false
	}
	return p.start < totalSectors && p.count <= totalSectors-p.start
}

func (p mbrPartition) end() uint64 { return p.start + p.count }

func readMBR(r io.ReaderAt) ([]byte, bool) {
	b := make([]byte, 512)
	if _, err := r.ReadAt(b, 0); err != nil {
		return nil, false
	}
	return b, b[510] == 0x55 && b[511] == 0xaa
}

func imageSectors(sourceSize uint64) uint64 {
	return (sourceSize + 511) / 512
}

// hasValidHybridMBR accepts a partition table whose non-covering entries are
// in-bounds, non-overlapping, and ascending, and which describes the optical
// image payload rather than an arbitrary in-bounds range planted in the ISO
// system area: either a whole-image entry is present or the last partition
// ends exactly at the end of the image.
func hasValidHybridMBR(r io.ReaderAt, sourceSize uint64) bool {
	b, ok := readMBR(r)
	if !ok {
		return false
	}
	totalSectors := imageSectors(sourceSize)
	var previousEnd uint64
	var coversWholeImage bool
	for i := 446; i < 510; i += 16 {
		p := parseMBRPartition(b[i : i+16])
		if p.empty() {
			continue
		}
		if !p.validBootFlag() {
			return false
		}
		if p.coversImage(totalSectors) {
			coversWholeImage = true
			continue
		}
		if !p.inBounds(totalSectors) || p.start < previousEnd {
			return false
		}
		previousEnd = p.end()
	}
	return coversWholeImage || previousEnd == totalSectors
}
