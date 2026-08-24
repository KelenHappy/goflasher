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
	"hash"
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
	// SplitPreflight may inject the application's platform WIM probe. Nil
	// uses wim.Probe. It is evaluated before a split-WIM plan can succeed.
	SplitPreflight func(context.Context) error
}

// BuildPlanInput groups the immutable source data used during preflight.
type BuildPlanInput struct {
	Source     io.ReaderAt
	SourceSize uint64
	Manifest   installeriso.Manifest
	Options    PlanOptions
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

type planBuilder struct {
	ctx        context.Context
	source     io.ReaderAt
	sourceSize uint64
	entries    []installeriso.Entry
	options    PlanOptions
	splitSize  uint64
}

type fileInspection struct {
	regular, fileClusters, directoryClusters, directoryBytes uint64
	verification                                             []VerificationEntry
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
// and part count come from the backend's already validated output, rather than
// the source-size estimate used by Preview.
func (p *BuildPlan) withPreparedSplitGeometry(sizes []uint64) (*BuildPlan, error) {
	if err := p.validatePreparedSplitSizes(sizes); err != nil {
		return nil, err
	}
	wimEntry, _ := plannedBySource(p, "sources/install.wim")
	actualClusters := splitFileClusters(sizes, p.fat.ClusterSize)
	estimatedClusters := estimatedSplitFileClusters(wimEntry.source.Size, p.splitSize, p.splitParts, p.fat.ClusterSize)
	q := p.clone()
	q.splitParts = len(sizes)
	q.adjustSplitFileClusters(actualClusters, estimatedClusters)
	q.adjustSplitDirectorySize(len(sizes))
	q.fat.AllocationBytes = (q.fat.FileClusters+q.fat.DirectoryClusters)*q.fat.ClusterSize + q.fat.FATBytes + 32*logicalSectorSize
	if q.fat.AllocationBytes > q.esp.Size {
		return nil, fmt.Errorf("%w: %w: prepared split set needs %d bytes, ESP has %d", ErrNoSpace, ErrTargetTooSmall, q.fat.AllocationBytes, q.esp.Size)
	}
	return q, nil
}

func (p *BuildPlan) validatePreparedSplitSizes(sizes []uint64) error {
	if !p.acceptsPreparedSplit(sizes) {
		return fmt.Errorf("%w: missing prepared split geometry", ErrVerification)
	}
	if _, ok := plannedBySource(p, "sources/install.wim"); !ok {
		return fmt.Errorf("%w: planned WIM is missing", ErrVerification)
	}
	for _, size := range sizes {
		if !p.validSplitPartSize(size) {
			return fmt.Errorf("%w: invalid prepared split part size", ErrVerification)
		}
	}
	return nil
}

func (p *BuildPlan) acceptsPreparedSplit(sizes []uint64) bool {
	return p != nil && p.strategy == SplitWIM && len(sizes) != 0
}

func (p *BuildPlan) validSplitPartSize(size uint64) bool {
	return size != 0 && size <= p.splitSize && size <= maxFATFileSize
}

func splitFileClusters(sizes []uint64, clusterSize uint64) (clusters uint64) {
	for _, size := range sizes {
		clusters += ceilDiv(size, clusterSize)
	}
	return clusters
}

func estimatedSplitFileClusters(remaining, splitSize uint64, parts int, clusterSize uint64) (clusters uint64) {
	for range parts {
		size := min(remaining, splitSize)
		clusters += ceilDiv(size, clusterSize)
		remaining -= size
	}
	return clusters
}

func (p *BuildPlan) clone() *BuildPlan {
	q := *p
	q.manifest = cloneManifest(p.manifest)
	q.verification = append([]VerificationEntry(nil), p.verification...)
	q.planned = append([]plannedEntry(nil), p.planned...)
	return &q
}

func (p *BuildPlan) adjustSplitFileClusters(actualClusters, estimatedClusters uint64) {
	if actualClusters >= estimatedClusters {
		p.fat.FileClusters += actualClusters - estimatedClusters
	} else {
		p.fat.FileClusters -= estimatedClusters - actualClusters
	}
}

func (p *BuildPlan) adjustSplitDirectorySize(parts int) {
	oldEntryBytes := fatDirectoryEntryBytes("install.wim")
	var actualEntryBytes uint64
	for i := range parts {
		name := "install.swm"
		if i > 0 {
			name = "install" + strconv.Itoa(i+1) + ".swm"
		}
		actualEntryBytes += fatDirectoryEntryBytes(name)
	}
	if actualEntryBytes > oldEntryBytes {
		delta := actualEntryBytes - oldEntryBytes
		p.fat.DirectoryBytes += delta
		p.fat.DirectoryClusters += ceilDiv(delta, p.fat.ClusterSize)
	}
}

func fatDirectoryEntryBytes(name string) uint64 {
	return uint64(32 * (2 + (len([]rune(name))+12)/13))
}

// NewBuildPlan performs the complete, non-destructive preflight. Input's source
// size is the immutable ISO descriptor's size, not a pathname stat made later.
func NewBuildPlan(ctx context.Context, input BuildPlanInput) (*BuildPlan, error) {
	builder := planBuilder{ctx: ctx, source: input.Source, sourceSize: input.SourceSize, entries: cloneManifest(input.Manifest).Entries, options: input.Options}
	return builder.build()
}

func (b *planBuilder) build() (*BuildPlan, error) {
	if err := b.validateInput(); err != nil {
		return nil, err
	}
	sort.Slice(b.entries, func(i, j int) bool { return strings.ToLower(b.entries[i].Path) < strings.ToLower(b.entries[j].Path) })
	planned, err := approveDestinations(b.entries)
	if err != nil {
		return nil, err
	}
	files := manifestFiles(b.entries)
	if _, ok := files["efi/boot/bootx64.efi"]; !ok {
		return nil, fmt.Errorf("%w: %w: efi/boot/bootx64.efi", ErrUnsupported, ErrMissingUEFILoader)
	}
	strategy, installSize, splitParts, err := selectInstallStrategy(files, b.splitSize)
	if err != nil {
		return nil, err
	}
	if err := b.preflightSplit(strategy); err != nil {
		return nil, err
	}
	return b.assemble(planned, strategy, installSize, splitParts)
}

func (b *planBuilder) validateInput() error {
	if !b.hasRequiredInput() {
		return fmt.Errorf("%w: incomplete preflight input", ErrUnsupported)
	}
	b.splitSize = b.options.SplitWIMPolicySize
	if b.splitSize == 0 {
		b.splitSize = defaultSplitSize
	}
	if b.splitSize > maxFATFileSize {
		return fmt.Errorf("%w: invalid split WIM policy", ErrUnsupported)
	}
	return nil
}

func (b *planBuilder) hasRequiredInput() bool {
	return b.source != nil && b.sourceSize != 0 && b.options.SourceIdentity != "" && b.options.TargetSize != 0
}

func manifestFiles(entries []installeriso.Entry) map[string]installeriso.Entry {
	files := make(map[string]installeriso.Entry)
	for _, entry := range entries {
		if entry.Type == installeriso.File {
			files[strings.ToLower(entry.Path)] = entry
		}
	}
	return files
}

func (b *planBuilder) preflightSplit(strategy InstallStrategy) error {
	if strategy != SplitWIM {
		return nil
	}
	preflight := b.options.SplitPreflight
	if preflight == nil {
		preflight = defaultSplitPreflight
	}
	if err := preflight(b.ctx); err != nil {
		return fmt.Errorf("%w: split WIM pipeline: %v", ErrUnsupported, err)
	}
	return nil
}

func defaultSplitPreflight(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return wim.Probe()
}

func (b *planBuilder) assemble(planned []plannedEntry, strategy InstallStrategy, installSize uint64, splitParts int) (*BuildPlan, error) {
	if b.options.TargetSize <= partitionStart+33*logicalSectorSize {
		return nil, errors.Join(ErrNoSpace, ErrTargetTooSmall)
	}
	espSize := b.options.TargetSize - partitionStart - 33*logicalSectorSize
	clusterSize := fatClusterSize(espSize)
	inspection, err := b.inspectFiles(clusterSize, strategy, installSize, splitParts)
	if err != nil {
		return nil, err
	}
	dataClusters := inspection.fileClusters + inspection.directoryClusters
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
	if temporary > b.options.TemporarySpace {
		return nil, fmt.Errorf("%w: %w: split WIM needs %d temporary bytes", ErrNoSpace, ErrTemporarySpace, temporary)
	}
	p := &BuildPlan{sourceIdentity: b.options.SourceIdentity, manifest: installeriso.Manifest{Entries: b.entries}, architecture: UEFIX64,
		esp:          ESPLayout{logicalSectorSize, partitionStart, espSize, gptBytes},
		fat:          FATLayoutEstimate{clusterSize, inspection.fileClusters, inspection.directoryClusters, fatBytes, inspection.directoryBytes, allocation},
		regularBytes: inspection.regular, strategy: strategy, splitSize: b.splitSize, splitParts: splitParts, temporaryBytes: temporary, verification: inspection.verification, planned: planned}
	p.sourceSHA256, err = hashRange(b.ctx, b.source, 0, b.sourceSize)
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
		destination, err := approveDestination(e)
		if err != nil {
			return nil, err
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

func approveDestination(entry installeriso.Entry) (string, error) {
	destination := entry.DestinationFATPath
	if destination == "" {
		destination = entry.Path
	}
	destination = strings.ReplaceAll(destination, "\\", "/")
	clean := path.Clean(destination)
	if unsafeDestination(destination, clean) {
		return "", fmt.Errorf("%w: unsafe destination path %q", ErrUnsupported, destination)
	}
	return destination, nil
}

func unsafeDestination(destination, clean string) bool {
	if destination == "" || path.IsAbs(destination) || clean != destination {
		return true
	}
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return true
	}
	return strings.ContainsRune(destination, 0)
}

func selectInstallStrategy(files map[string]installeriso.Entry, splitSize uint64) (InstallStrategy, uint64, int, error) {
	_, hasWIM := files["sources/install.wim"]
	_, hasESD := files["sources/install.esd"]
	swm, err := collectExistingSWM(files)
	if err != nil {
		return "", 0, 0, err
	}
	if len(swm) != 0 {
		return existingSWMStrategy(swm, hasWIM, hasESD)
	}
	return singleInstallImageStrategy(files, splitSize)
}

func collectExistingSWM(files map[string]installeriso.Entry) (map[int]installeriso.Entry, error) {
	swm := map[int]installeriso.Entry{}
	for name, e := range files {
		if strings.HasPrefix(name, "sources/install") && strings.HasSuffix(name, ".swm") {
			n, ok := swmSequence(name)
			if !ok {
				return nil, fmt.Errorf("%w: invalid existing SWM name %q", ErrUnsupported, name)
			}
			if _, duplicate := swm[n]; duplicate {
				return nil, fmt.Errorf("%w: duplicate existing SWM sequence %d", ErrUnsupported, n)
			}
			swm[n] = e
		}
	}
	return swm, nil
}

func existingSWMStrategy(swm map[int]installeriso.Entry, hasWIM, hasESD bool) (InstallStrategy, uint64, int, error) {
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

func singleInstallImageStrategy(files map[string]installeriso.Entry, splitSize uint64) (InstallStrategy, uint64, int, error) {
	wim, hasWIM := files["sources/install.wim"]
	esd, hasESD := files["sources/install.esd"]
	if hasWIM && hasESD {
		return "", 0, 0, fmt.Errorf("%w: multiple install images", ErrUnsupported)
	}
	if hasWIM {
		return wimStrategy(wim, splitSize)
	}
	if !hasESD {
		return NoInstallImage, 0, 0, nil
	}
	if esd.Size > maxFATFileSize {
		return "", 0, 0, fmt.Errorf("%w: install.esd exceeds FAT32 file limit", ErrUnsupported)
	}
	return CopyESD, esd.Size, 1, nil
}

func wimStrategy(wim installeriso.Entry, splitSize uint64) (InstallStrategy, uint64, int, error) {
	if wim.Size <= maxFATFileSize {
		return CopyWIM, wim.Size, 1, nil
	}
	return SplitWIM, wim.Size, int((wim.Size + splitSize - 1) / splitSize), nil
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

func (b *planBuilder) inspectFiles(cluster uint64, strategy InstallStrategy, installSize uint64, splitParts int) (fileInspection, error) {
	var result fileInspection
	dirBytes := map[string]uint64{"": 96}
	for _, entry := range b.entries {
		if entry.Type == installeriso.Directory {
			accountDirectoryEntry(dirBytes, entry)
			continue
		}
		if entry.Type != installeriso.File {
			continue
		}
		if err := validateFATFileSize(entry, strategy); err != nil {
			return fileInspection{}, err
		}
		lower := strings.ToLower(entry.Path)
		if !isInstallImage(lower) {
			result.regular += entry.Size
		}
		result.fileClusters += b.entryClusters(entry, lower, cluster, strategy, installSize, splitParts)
		dirBytes[parentKey(lower)] += fatDirectoryEntryBytes(path.Base(entry.Path))
		hash, err := b.hashExtents(entry)
		if err != nil {
			return fileInspection{}, err
		}
		result.verification = append(result.verification, VerificationEntry{verificationDestination(entry), entry.Size, hash})
	}
	for _, bytes := range dirBytes {
		result.directoryBytes += bytes
		result.directoryClusters += ceilDiv(bytes, cluster)
	}
	return result, nil
}

func accountDirectoryEntry(dirBytes map[string]uint64, entry installeriso.Entry) {
	lower := strings.ToLower(entry.Path)
	dirBytes[lower] += 64
	dirBytes[parentKey(lower)] += fatDirectoryEntryBytes(path.Base(entry.Path))
}

func validateFATFileSize(entry installeriso.Entry, strategy InstallStrategy) error {
	if entry.Size <= maxFATFileSize || strategy == SplitWIM && strings.EqualFold(entry.Path, "sources/install.wim") {
		return nil
	}
	return fmt.Errorf("%w: %s exceeds FAT32 file limit", ErrUnsupported, entry.Path)
}

func isInstallImage(lower string) bool {
	return lower == "sources/install.wim" || lower == "sources/install.esd" || strings.HasPrefix(lower, "sources/install") && strings.HasSuffix(lower, ".swm")
}

func (b *planBuilder) entryClusters(entry installeriso.Entry, lower string, cluster uint64, strategy InstallStrategy, installSize uint64, splitParts int) uint64 {
	if strategy != SplitWIM || lower != "sources/install.wim" {
		return ceilDiv(entry.Size, cluster)
	}
	return estimatedSplitFileClusters(installSize, b.splitSize, splitParts, cluster)
}

func verificationDestination(entry installeriso.Entry) string {
	if entry.DestinationFATPath != "" {
		return entry.DestinationFATPath
	}
	return entry.Path
}

func (b *planBuilder) hashExtents(e installeriso.Entry) (string, error) {
	h := extentHasher{builder: b, digest: sha256.New(), buffer: make([]byte, 1<<20)}
	return h.sum(e)
}

type extentHasher struct {
	builder *planBuilder
	digest  hash.Hash
	buffer  []byte
}

func (h *extentHasher) sum(entry installeriso.Entry) (string, error) {
	remaining := entry.Size
	for _, extent := range entry.Extents {
		n := min(remaining, extent.Length)
		if err := h.add(extent.Offset, n); err != nil {
			return "", err
		}
		remaining -= n
	}
	if remaining != 0 {
		return "", fmt.Errorf("%w: incomplete extent for %s", ErrUnsupported, entry.Path)
	}
	return hex.EncodeToString(h.digest.Sum(nil)), nil
}

func (h *extentHasher) add(offset, size uint64) error {
	if offset > h.builder.sourceSize || size > h.builder.sourceSize-offset {
		return fmt.Errorf("%w: extent out of bounds", ErrUnsupported)
	}
	for done := uint64(0); done < size; {
		if err := h.builder.ctx.Err(); err != nil {
			return err
		}
		take := min(uint64(len(h.buffer)), size-done)
		if _, err := h.builder.source.ReadAt(h.buffer[:take], int64(offset+done)); err != nil {
			return err
		}
		_, _ = h.digest.Write(h.buffer[:take])
		done += take
	}
	return nil
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
