// Package installer plans construction of Windows installer media. Planning is
// deliberately read-only: callers must obtain a complete BuildPlan before any
// target backend is asked to unmount, open, format, or write a device.
package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"

	installeriso "github.com/goflasher/goflasher/internal/installer/iso"
	"github.com/goflasher/goflasher/internal/wim"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	logicalSectorSize     = uint64(512)
	partitionStart        = uint64(1 << 20)
	maxFATFileSize        = uint64(math.MaxUint32)
	defaultSplitSize      = uint64(3800 << 20)
	splitTempBaseOverhead = uint64(64 << 20)
)

var (
	ErrUnsupported       = errors.New("unsupported Windows installer media")
	ErrMissingUEFILoader = errors.New("required UEFI x64 loader is missing")
	ErrTargetTooSmall    = errors.New("target USB device is too small")
	ErrTemporarySpace    = errors.New("insufficient temporary disk space")
	ErrNoSpace           = errors.New("insufficient target or temporary space")
	ErrWIMSplitFailure   = errors.New("WIM split failed")
)

type Architecture string

const UEFIX64 Architecture = "uefi-x64"

type InstallStrategy string

const (
	NoInstallImage  InstallStrategy = "none"
	CopyWIM         InstallStrategy = "copy-wim"
	CopyESD         InstallStrategy = "copy-esd"
	SplitWIM        InstallStrategy = "split-wim"
	CopyExistingSWM InstallStrategy = "copy-existing-swm"
)

// PlanOptions describes non-source constraints known during preflight.
type PlanOptions struct {
	SourceIdentity     string
	TargetSize         uint64
	TemporarySpace     uint64
	SplitWIMPolicySize uint64
	// SplitPreflight may inject the application's bundled-libwim probe. Nil
	// uses wim.Probe. It is evaluated before a split-WIM plan can succeed.
	SplitPreflight func(context.Context) error
}

type ESPLayout struct {
	LogicalSectorSize uint64
	StartOffset       uint64
	Size              uint64
	GPTMetadataBytes  uint64
}

type FATLayoutEstimate struct {
	ClusterSize       uint64
	FileClusters      uint64
	DirectoryClusters uint64
	FATBytes          uint64
	DirectoryBytes    uint64
	AllocationBytes   uint64
}

type VerificationEntry struct {
	Path   string
	Size   uint64
	SHA256 string
}

// BuildPlan is immutable after construction. Accessors return values or deep
// copies so execution cannot silently diverge from the successful preflight.
type BuildPlan struct {
	sourceIdentity string
	sourceSHA256   string
	manifest       installeriso.Manifest
	architecture   Architecture
	esp            ESPLayout
	fat            FATLayoutEstimate
	regularBytes   uint64
	strategy       InstallStrategy
	splitSize      uint64
	splitParts     int
	temporaryBytes uint64
	verification   []VerificationEntry
	planned        []plannedEntry
}

type plannedEntry struct {
	source      installeriso.Entry
	destination string
}

func (p *BuildPlan) SourceIdentity() string               { return p.sourceIdentity }
func (p *BuildPlan) SourceSHA256() string                 { return p.sourceSHA256 }
func (p *BuildPlan) Architecture() Architecture           { return p.architecture }
func (p *BuildPlan) ESPLayout() ESPLayout                 { return p.esp }
func (p *BuildPlan) FATLayoutEstimate() FATLayoutEstimate { return p.fat }
func (p *BuildPlan) RegularFileBytes() uint64             { return p.regularBytes }
func (p *BuildPlan) InstallStrategy() InstallStrategy     { return p.strategy }
func (p *BuildPlan) SplitWIMPolicySize() uint64           { return p.splitSize }
func (p *BuildPlan) TemporarySpaceRequired() uint64       { return p.temporaryBytes }
func (p *BuildPlan) RequiredTargetCapacity() uint64 {
	return p.esp.StartOffset + p.esp.Size + 33*logicalSectorSize
}
func (p *BuildPlan) Manifest() installeriso.Manifest { return cloneManifest(p.manifest) }
func (p *BuildPlan) VerificationManifest() []VerificationEntry {
	return append([]VerificationEntry(nil), p.verification...)
}

// withPreparedSplitGeometry returns a new immutable plan whose FAT allocation
// and part count come from libwim's already validated output, rather than the
// source-size estimate used by Preview.
func (p *BuildPlan) withPreparedSplitGeometry(sizes []uint64) (*BuildPlan, error) {
	if p == nil || p.strategy != SplitWIM || len(sizes) == 0 {
		return nil, fmt.Errorf("%w: missing prepared split geometry", ErrVerification)
	}
	wimEntry, ok := plannedBySource(p, "sources/install.wim")
	if !ok {
		return nil, fmt.Errorf("%w: planned WIM is missing", ErrVerification)
	}
	var actualClusters uint64
	for _, size := range sizes {
		if size == 0 || size > p.splitSize || size > maxFATFileSize {
			return nil, fmt.Errorf("%w: invalid prepared split part size", ErrVerification)
		}
		actualClusters += ceilDiv(size, p.fat.ClusterSize)
	}
	var estimatedClusters uint64
	remaining := wimEntry.source.Size
	for i := 0; i < p.splitParts; i++ {
		size := min(remaining, p.splitSize)
		estimatedClusters += ceilDiv(size, p.fat.ClusterSize)
		remaining -= size
	}
	q := *p
	q.manifest = cloneManifest(p.manifest)
	q.verification = append([]VerificationEntry(nil), p.verification...)
	q.planned = append([]plannedEntry(nil), p.planned...)
	q.splitParts = len(sizes)
	if actualClusters >= estimatedClusters {
		q.fat.FileClusters += actualClusters - estimatedClusters
	} else {
		q.fat.FileClusters -= estimatedClusters - actualClusters
	}
	// The initial plan accounts for the original install.wim directory entry.
	// Add the extra entries produced by the actual canonical SWM set.
	oldEntryBytes := fatDirectoryEntryBytes("install.wim")
	var actualEntryBytes uint64
	for i := range sizes {
		name := "install.swm"
		if i > 0 {
			name = "install" + strconv.Itoa(i+1) + ".swm"
		}
		actualEntryBytes += fatDirectoryEntryBytes(name)
	}
	if actualEntryBytes > oldEntryBytes {
		delta := actualEntryBytes - oldEntryBytes
		q.fat.DirectoryBytes += delta
		q.fat.DirectoryClusters += ceilDiv(delta, q.fat.ClusterSize)
	}
	q.fat.AllocationBytes = (q.fat.FileClusters+q.fat.DirectoryClusters)*q.fat.ClusterSize + q.fat.FATBytes + 32*logicalSectorSize
	if q.fat.AllocationBytes > q.esp.Size {
		return nil, fmt.Errorf("%w: %w: prepared split set needs %d bytes, ESP has %d", ErrNoSpace, ErrTargetTooSmall, q.fat.AllocationBytes, q.esp.Size)
	}
	return &q, nil
}

func fatDirectoryEntryBytes(name string) uint64 {
	return uint64(32 * (2 + (len([]rune(name))+12)/13))
}

// NewBuildPlan performs the complete, non-destructive preflight. sourceSize is
// the immutable ISO descriptor's size, not a pathname stat made later.
func NewBuildPlan(ctx context.Context, source io.ReaderAt, sourceSize uint64, manifest installeriso.Manifest, options PlanOptions) (*BuildPlan, error) {
	if source == nil || sourceSize == 0 || options.SourceIdentity == "" || options.TargetSize == 0 {
		return nil, fmt.Errorf("%w: incomplete preflight input", ErrUnsupported)
	}
	splitSize := options.SplitWIMPolicySize
	if splitSize == 0 {
		splitSize = defaultSplitSize
	}
	if splitSize == 0 || splitSize > maxFATFileSize {
		return nil, fmt.Errorf("%w: invalid split WIM policy", ErrUnsupported)
	}
	entries := cloneManifest(manifest).Entries
	sort.Slice(entries, func(i, j int) bool { return strings.ToLower(entries[i].Path) < strings.ToLower(entries[j].Path) })
	planned, err := approveDestinations(entries)
	if err != nil {
		return nil, err
	}
	files := make(map[string]installeriso.Entry)
	for _, e := range entries {
		if e.Type == installeriso.File {
			files[strings.ToLower(e.Path)] = e
		}
	}
	if _, ok := files["efi/boot/bootx64.efi"]; !ok {
		return nil, fmt.Errorf("%w: %w: efi/boot/bootx64.efi", ErrUnsupported, ErrMissingUEFILoader)
	}
	strategy, installSize, splitParts, err := selectInstallStrategy(files, splitSize)
	if err != nil {
		return nil, err
	}
	if strategy == SplitWIM {
		preflight := options.SplitPreflight
		if preflight == nil {
			preflight = func(ctx context.Context) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				return wim.Probe()
			}
		}
		if err := preflight(ctx); err != nil {
			return nil, fmt.Errorf("%w: split WIM pipeline: %v", ErrUnsupported, err)
		}
	}
	if options.TargetSize <= partitionStart+33*logicalSectorSize {
		return nil, errors.Join(ErrNoSpace, ErrTargetTooSmall)
	}
	espSize := options.TargetSize - partitionStart - 33*logicalSectorSize
	clusterSize := fatClusterSize(espSize)
	regular, fileClusters, dirClusters, directoryBytes, verification, err := inspectFiles(ctx, source, sourceSize, entries, clusterSize, strategy, installSize, splitParts, splitSize)
	if err != nil {
		return nil, err
	}
	dataClusters := fileClusters + dirClusters
	fatBytes := estimateFATBytes(espSize, clusterSize)
	allocation := dataClusters*clusterSize + fatBytes + 32*logicalSectorSize
	gptBytes := uint64(67) * logicalSectorSize // protective MBR, both headers, and both entry arrays
	if allocation > espSize {
		return nil, fmt.Errorf("%w: %w: need %d bytes, ESP has %d", ErrNoSpace, ErrTargetTooSmall, allocation, espSize)
	}
	temporary := uint64(0)
	if strategy == SplitWIM {
		temporary, err = estimateSplitTemporarySpace(installSize, splitParts)
		if err != nil {
			return nil, err
		}
	}
	if temporary > options.TemporarySpace {
		return nil, fmt.Errorf("%w: %w: split WIM needs %d temporary bytes", ErrNoSpace, ErrTemporarySpace, temporary)
	}
	p := &BuildPlan{sourceIdentity: options.SourceIdentity, manifest: installeriso.Manifest{Entries: entries}, architecture: UEFIX64,
		esp:          ESPLayout{logicalSectorSize, partitionStart, espSize, gptBytes},
		fat:          FATLayoutEstimate{clusterSize, fileClusters, dirClusters, fatBytes, directoryBytes, allocation},
		regularBytes: regular, strategy: strategy, splitSize: splitSize, splitParts: splitParts, temporaryBytes: temporary, verification: verification, planned: planned}
	p.sourceSHA256, err = hashRange(ctx, source, 0, sourceSize)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// estimateSplitTemporarySpace reserves the simultaneously retained staged WIM,
// the maximum accepted SWM output set, and conservative filesystem metadata.
func estimateSplitTemporarySpace(sourceSize uint64, parts int) (uint64, error) {
	if sourceSize == 0 || parts <= 0 {
		return 0, fmt.Errorf("%w: invalid split temporary-space input", ErrUnsupported)
	}
	quarter := sourceSize / 4
	if sourceSize > math.MaxUint64-quarter {
		return 0, fmt.Errorf("%w: split temporary-space overflow", ErrUnsupported)
	}
	output := sourceSize + quarter
	partsU := uint64(parts)
	if partsU > math.MaxUint64/(1<<20) {
		return 0, fmt.Errorf("%w: split temporary-space overflow", ErrUnsupported)
	}
	partOverhead := partsU * (1 << 20)
	if output > math.MaxUint64-partOverhead {
		return 0, fmt.Errorf("%w: split temporary-space overflow", ErrUnsupported)
	}
	output += partOverhead
	if sourceSize > math.MaxUint64-output {
		return 0, fmt.Errorf("%w: split temporary-space overflow", ErrUnsupported)
	}
	total := sourceSize + output
	filesystemOverhead := total/100 + splitTempBaseOverhead
	if total > math.MaxUint64-filesystemOverhead {
		return 0, fmt.Errorf("%w: split temporary-space overflow", ErrUnsupported)
	}
	return total + filesystemOverhead, nil
}

func approveDestinations(entries []installeriso.Entry) ([]plannedEntry, error) {
	approved := make([]plannedEntry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		destination := e.DestinationFATPath
		if destination == "" {
			destination = e.Path
		}
		destination = strings.ReplaceAll(destination, "\\", "/")
		if destination == "" || path.IsAbs(destination) || path.Clean(destination) != destination || destination == "." || destination == ".." || strings.HasPrefix(destination, "../") || strings.ContainsRune(destination, 0) {
			return nil, fmt.Errorf("%w: unsafe destination path %q", ErrUnsupported, destination)
		}
		key := norm.NFC.String(cases.Fold().String(destination))
		if seen[key] {
			return nil, fmt.Errorf("%w: destination collision %q", ErrUnsupported, destination)
		}
		seen[key] = true
		approved = append(approved, plannedEntry{source: e, destination: destination})
	}
	return approved, nil
}

func selectInstallStrategy(files map[string]installeriso.Entry, splitSize uint64) (InstallStrategy, uint64, int, error) {
	wim, hasWIM := files["sources/install.wim"]
	esd, hasESD := files["sources/install.esd"]
	swm := map[int]installeriso.Entry{}
	for name, e := range files {
		if strings.HasPrefix(name, "sources/install") && strings.HasSuffix(name, ".swm") {
			n, ok := swmSequence(name)
			if !ok {
				return "", 0, 0, fmt.Errorf("%w: invalid existing SWM name %q", ErrUnsupported, name)
			}
			if _, duplicate := swm[n]; duplicate {
				return "", 0, 0, fmt.Errorf("%w: duplicate existing SWM sequence %d", ErrUnsupported, n)
			}
			swm[n] = e
		}
	}
	if len(swm) > 0 {
		if hasWIM || hasESD {
			return "", 0, 0, fmt.Errorf("%w: existing SWM set cannot be mixed with WIM or ESD", ErrUnsupported)
		}
		for i := 1; i <= len(swm); i++ {
			if _, ok := swm[i]; !ok {
				return "", 0, 0, fmt.Errorf("%w: existing SWM set is not contiguous", ErrUnsupported)
			}
		}
		return CopyExistingSWM, 0, len(swm), nil
	}
	if hasWIM && hasESD {
		return "", 0, 0, fmt.Errorf("%w: multiple install images", ErrUnsupported)
	}
	if hasWIM {
		if wim.Size <= maxFATFileSize {
			return CopyWIM, wim.Size, 1, nil
		}
		return SplitWIM, wim.Size, int((wim.Size + splitSize - 1) / splitSize), nil
	}
	if hasESD {
		if esd.Size > maxFATFileSize {
			return "", 0, 0, fmt.Errorf("%w: install.esd exceeds FAT32 file limit", ErrUnsupported)
		}
		return CopyESD, esd.Size, 1, nil
	}
	return NoInstallImage, 0, 0, nil
}

func swmSequence(name string) (int, bool) {
	if name == "sources/install.swm" {
		return 1, true
	}
	if !strings.HasPrefix(name, "sources/install") || !strings.HasSuffix(name, ".swm") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "sources/install"), ".swm"))
	return n, err == nil && n >= 2 && name == "sources/install"+strconv.Itoa(n)+".swm"
}

func inspectFiles(ctx context.Context, source io.ReaderAt, sourceSize uint64, entries []installeriso.Entry, cluster uint64, strategy InstallStrategy, installSize uint64, splitParts int, splitSize uint64) (uint64, uint64, uint64, uint64, []VerificationEntry, error) {
	var regular, fileClusters uint64
	dirBytes := map[string]uint64{"": 96}
	var verify []VerificationEntry
	for _, e := range entries {
		if e.Type == installeriso.Directory {
			lower := strings.ToLower(e.Path)
			dirBytes[lower] += 64
			dirBytes[parentKey(lower)] += uint64(32 * (2 + (len([]rune(path.Base(e.Path)))+12)/13))
			continue
		}
		if e.Type != installeriso.File {
			continue
		}
		if e.Size > maxFATFileSize && !(strategy == SplitWIM && strings.EqualFold(e.Path, "sources/install.wim")) {
			return 0, 0, 0, 0, nil, fmt.Errorf("%w: %s exceeds FAT32 file limit", ErrUnsupported, e.Path)
		}
		lower := strings.ToLower(e.Path)
		isInstall := lower == "sources/install.wim" || lower == "sources/install.esd" || strings.HasSuffix(lower, ".swm") && strings.HasPrefix(lower, "sources/install")
		if !isInstall {
			regular += e.Size
		}
		if strategy == SplitWIM && lower == "sources/install.wim" {
			remaining := installSize
			for i := 0; i < splitParts; i++ {
				n := min(remaining, splitSize)
				fileClusters += ceilDiv(n, cluster)
				remaining -= n
			}
		} else {
			fileClusters += ceilDiv(e.Size, cluster)
		}
		dirBytes[parentKey(strings.ToLower(e.Path))] += uint64(32 * (2 + (len([]rune(path.Base(e.Path)))+12)/13))
		h, err := hashExtents(ctx, source, sourceSize, e)
		if err != nil {
			return 0, 0, 0, 0, nil, err
		}
		destination := e.DestinationFATPath
		if destination == "" {
			destination = e.Path
		}
		verify = append(verify, VerificationEntry{destination, e.Size, h})
	}
	var directoryBytes, directoryClusters uint64
	for _, n := range dirBytes {
		directoryBytes += n
		directoryClusters += ceilDiv(n, cluster)
	}
	return regular, fileClusters, directoryClusters, directoryBytes, verify, nil
}

func hashExtents(ctx context.Context, source io.ReaderAt, sourceSize uint64, e installeriso.Entry) (string, error) {
	h := sha256.New()
	remaining := e.Size
	buf := make([]byte, 1<<20)
	for _, extent := range e.Extents {
		n := min(remaining, extent.Length)
		if extent.Offset > sourceSize || n > sourceSize-extent.Offset {
			return "", fmt.Errorf("%w: extent out of bounds", ErrUnsupported)
		}
		for off := uint64(0); off < n; {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			take := min(uint64(len(buf)), n-off)
			if _, err := source.ReadAt(buf[:take], int64(extent.Offset+off)); err != nil {
				return "", err
			}
			h.Write(buf[:take])
			off += take
		}
		remaining -= n
	}
	if remaining != 0 {
		return "", fmt.Errorf("%w: incomplete extent for %s", ErrUnsupported, e.Path)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashRange(ctx context.Context, source io.ReaderAt, off, size uint64) (string, error) {
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for done := uint64(0); done < size; {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n := min(uint64(len(buf)), size-done)
		if _, err := source.ReadAt(buf[:n], int64(off+done)); err != nil {
			return "", err
		}
		h.Write(buf[:n])
		done += n
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func cloneManifest(m installeriso.Manifest) installeriso.Manifest {
	out := installeriso.Manifest{Entries: make([]installeriso.Entry, len(m.Entries))}
	for i, e := range m.Entries {
		out.Entries[i] = e
		out.Entries[i].Extents = append([]installeriso.Extent(nil), e.Extents...)
	}
	return out
}
func parentKey(name string) string {
	p := path.Dir(name)
	if p == "." || p == "/" {
		return ""
	}
	return p
}
func estimateFATBytes(volumeSize, clusterSize uint64) uint64 {
	// FAT entries themselves consume volume space, so converge on the number
	// of data clusters after both FAT copies and the reserved region.
	fatSectors := uint64(1)
	for {
		available := volumeSize/logicalSectorSize - 32 - 2*fatSectors
		clusters := available / (clusterSize / logicalSectorSize)
		required := ceilDiv((clusters+2)*4, logicalSectorSize)
		if required <= fatSectors {
			return fatSectors * logicalSectorSize * 2
		}
		fatSectors = required
	}
}
func fatClusterSize(size uint64) uint64 {
	switch {
	case size <= 260<<20:
		return 512
	case size <= 8<<30:
		return 4096
	case size <= 16<<30:
		return 8192
	case size <= 32<<30:
		return 16384
	default:
		return 32768
	}
}
func ceilDiv(n, d uint64) uint64 {
	if n == 0 {
		return 0
	}
	return 1 + (n-1)/d
}
func roundUp(n, unit uint64) uint64 { return ceilDiv(n, unit) * unit }
