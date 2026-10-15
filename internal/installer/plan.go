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
	defaultSplitSize      = splitPolicy(3800 << 20)
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
	splitSize  splitPolicy
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
func (p *BuildPlan) withPreparedSplitGeometry(sizes splitPartSizes) (*BuildPlan, error) {
	if err := p.validatePreparedSplitSizes(sizes); err != nil {
		return nil, err
	}
	wimEntry, _ := plannedBySource(p, "sources/install.wim")
	estimate := installLayout{strategy: SplitWIM, installSize: wimEntry.source.Size, splitParts: p.splitParts}
	q := p.clone()
	q.splitParts = len(sizes)
	q.adjustSplitFileClusters(sizes, estimate)
	q.adjustSplitDirectorySize(sizes)
	q.fat.AllocationBytes = q.fat.allocationBytes()
	if q.fat.AllocationBytes > q.esp.Size {
		return nil, fmt.Errorf("%w: %w: prepared split set needs %d bytes, ESP has %d", ErrNoSpace, ErrTargetTooSmall, q.fat.AllocationBytes, q.esp.Size)
	}
	return q, nil
}

func (p *BuildPlan) validatePreparedSplitSizes(sizes splitPartSizes) error {
	if !p.acceptsPreparedSplit(sizes) {
		return fmt.Errorf("%w: missing prepared split geometry", ErrVerification)
	}
	if _, ok := plannedBySource(p, "sources/install.wim"); !ok {
		return fmt.Errorf("%w: planned WIM is missing", ErrVerification)
	}
	if !splitPolicy(p.splitSize).admits(sizes) {
		return fmt.Errorf("%w: invalid prepared split part size", ErrVerification)
	}
	return nil
}

func (p *BuildPlan) acceptsPreparedSplit(sizes splitPartSizes) bool {
	return p != nil && p.strategy == SplitWIM && len(sizes) != 0
}

func (p *BuildPlan) clone() *BuildPlan {
	q := *p
	q.manifest = cloneManifest(p.manifest)
	q.verification = append([]VerificationEntry(nil), p.verification...)
	q.planned = append([]plannedEntry(nil), p.planned...)
	return &q
}

// adjustSplitFileClusters swaps the preview's estimated split allocation for
// the clusters the actual parts occupy.
func (p *BuildPlan) adjustSplitFileClusters(actual splitPartSizes, estimate installLayout) {
	cluster := p.fat.cluster()
	actualClusters := actual.clusters(cluster)
	estimatedClusters := estimate.estimatedSplitClusters(splitPolicy(p.splitSize), cluster)
	if actualClusters >= estimatedClusters {
		p.fat.FileClusters += actualClusters - estimatedClusters
	} else {
		p.fat.FileClusters -= estimatedClusters - actualClusters
	}
}

func (p *BuildPlan) adjustSplitDirectorySize(sizes splitPartSizes) {
	oldEntryBytes := fatName("install.wim").directoryEntryBytes()
	var actualEntryBytes uint64
	for i := range sizes {
		actualEntryBytes += fatName(splitPartName(i + 1)).directoryEntryBytes()
	}
	if actualEntryBytes > oldEntryBytes {
		delta := actualEntryBytes - oldEntryBytes
		p.fat.DirectoryBytes += delta
		p.fat.DirectoryClusters += p.fat.cluster().clusters(delta)
	}
}

// clusterSize is the FAT32 allocation unit in bytes.
type clusterSize uint64

// clusters is how many allocation units n bytes occupy.
func (c clusterSize) clusters(n uint64) uint64 { return ceilDiv(n, uint64(c)) }

func (f FATLayoutEstimate) cluster() clusterSize { return clusterSize(f.ClusterSize) }

// allocationBytes is the space the data clusters, both FATs, and the reserved
// region need on the ESP.
func (f FATLayoutEstimate) allocationBytes() uint64 {
	return (f.FileClusters+f.DirectoryClusters)*f.ClusterSize + f.FATBytes + 32*logicalSectorSize
}

func (l ESPLayout) clusterSize() clusterSize {
	switch {
	case l.Size <= 260<<20:
		return 512
	case l.Size <= 8<<30:
		return 4096
	case l.Size <= 16<<30:
		return 8192
	case l.Size <= 32<<30:
		return 16384
	default:
		return 32768
	}
}

func (l ESPLayout) fatBytes(cluster clusterSize) uint64 {
	// FAT entries themselves consume volume space, so converge on the number
	// of data clusters after both FAT copies and the reserved region.
	fatSectors := uint64(1)
	for {
		available := l.Size/logicalSectorSize - 32 - 2*fatSectors
		clusters := available / (uint64(cluster) / logicalSectorSize)
		required := ceilDiv((clusters+2)*4, logicalSectorSize)
		if required <= fatSectors {
			return fatSectors * logicalSectorSize * 2
		}
		fatSectors = required
	}
}

// fatName is one path component as it will be stored on the ESP.
type fatName string

// directoryEntryBytes is the directory space n occupies, long-name entries
// included.
func (n fatName) directoryEntryBytes() uint64 {
	return uint64(32 * (2 + (len([]rune(n))+12)/13))
}

// splitPartSizes are the byte sizes of a split WIM's parts, in sequence order.
type splitPartSizes []uint64

func (s splitPartSizes) clusters(cluster clusterSize) (clusters uint64) {
	for _, size := range s {
		clusters += cluster.clusters(size)
	}
	return clusters
}

// splitPolicy is the largest SWM part a split may produce.
type splitPolicy uint64

func (s splitPolicy) valid() bool { return uint64(s) <= maxFATFileSize }

// admits reports whether every part is non-empty and within both the policy
// and the FAT32 file limit.
func (s splitPolicy) admits(sizes splitPartSizes) bool {
	for _, size := range sizes {
		if size == 0 || size > uint64(s) || size > maxFATFileSize {
			return false
		}
	}
	return true
}

// wimLayout copies an install.wim FAT32 can hold and otherwise splits it into
// policy-sized parts.
func (s splitPolicy) wimLayout(wim installeriso.Entry) installLayout {
	if wim.Size <= maxFATFileSize {
		return installLayout{strategy: CopyWIM, installSize: wim.Size, splitParts: 1}
	}
	return installLayout{strategy: SplitWIM, installSize: wim.Size, splitParts: int(ceilDiv(wim.Size, uint64(s)))}
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
	sort.Slice(b.entries, func(i, j int) bool { return keyOf(b.entries[i]) < keyOf(b.entries[j]) })
	planned, err := approveDestinations(b.entries)
	if err != nil {
		return nil, err
	}
	files := indexFiles(b.entries)
	if !files.has(keyBootLoader) {
		return nil, fmt.Errorf("%w: %w: efi/boot/bootx64.efi", ErrUnsupported, ErrMissingUEFILoader)
	}
	layout, err := selectInstallStrategy(files, b.splitSize)
	if err != nil {
		return nil, err
	}
	if err := b.preflightSplit(layout.strategy); err != nil {
		return nil, err
	}
	return b.assemble(planned, layout)
}

func (b *planBuilder) validateInput() error {
	if !b.hasRequiredInput() {
		return fmt.Errorf("%w: incomplete preflight input", ErrUnsupported)
	}
	b.splitSize = splitPolicy(b.options.SplitWIMPolicySize)
	if b.splitSize == 0 {
		b.splitSize = defaultSplitSize
	}
	if !b.splitSize.valid() {
		return fmt.Errorf("%w: invalid split WIM policy", ErrUnsupported)
	}
	return nil
}

func (b *planBuilder) hasRequiredInput() bool {
	return b.source != nil && b.sourceSize != 0 && b.options.SourceIdentity != "" && b.options.TargetSize != 0
}

// manifestKey is a manifest path folded to lower case, the form install-image
// and loader lookups compare against.
type manifestKey string

const (
	keyBootLoader manifestKey = "efi/boot/bootx64.efi"
	keyInstallWIM manifestKey = "sources/install.wim"
	keyInstallESD manifestKey = "sources/install.esd"
	keyFirstSWM   manifestKey = "sources/install.swm"
)

func keyOf(entry installeriso.Entry) manifestKey { return manifestKey(strings.ToLower(entry.Path)) }

func (k manifestKey) looksLikeSWM() bool {
	return strings.HasPrefix(string(k), "sources/install") && strings.HasSuffix(string(k), ".swm")
}

func (k manifestKey) isInstallImage() bool {
	return k == keyInstallWIM || k == keyInstallESD || k.looksLikeSWM()
}

// swmSequence is the part number of an existing SWM name: install.swm is 1,
// installN.swm is N for N >= 2 written without leading zeros.
func (k manifestKey) swmSequence() (int, bool) {
	if k == keyFirstSWM {
		return 1, true
	}
	if !k.looksLikeSWM() {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(string(k), "sources/install"), ".swm"))
	return n, err == nil && n >= 2 && string(k) == "sources/install"+strconv.Itoa(n)+".swm"
}

// parent is the key of the directory holding k; the root is "".
func (k manifestKey) parent() manifestKey {
	p := path.Dir(string(k))
	if p == "." || p == "/" {
		return ""
	}
	return manifestKey(p)
}

// manifestFiles indexes the manifest's regular files by manifestKey.
type manifestFiles map[manifestKey]installeriso.Entry

func indexFiles(entries []installeriso.Entry) manifestFiles {
	files := make(manifestFiles)
	for _, entry := range entries {
		if entry.Type == installeriso.File {
			files[keyOf(entry)] = entry
		}
	}
	return files
}

func (f manifestFiles) has(k manifestKey) bool {
	_, ok := f[k]
	return ok
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

func (b *planBuilder) assemble(planned []plannedEntry, layout installLayout) (*BuildPlan, error) {
	esp, err := b.espLayout()
	if err != nil {
		return nil, err
	}
	cluster := esp.clusterSize()
	inspection, err := b.inspectFiles(cluster, layout)
	if err != nil {
		return nil, err
	}
	fat := FATLayoutEstimate{ClusterSize: uint64(cluster), FileClusters: inspection.fileClusters, DirectoryClusters: inspection.directoryClusters,
		FATBytes: esp.fatBytes(cluster), DirectoryBytes: inspection.directoryBytes}
	fat.AllocationBytes = fat.allocationBytes()
	if fat.AllocationBytes > esp.Size {
		return nil, fmt.Errorf("%w: %w: need %d bytes, ESP has %d", ErrNoSpace, ErrTargetTooSmall, fat.AllocationBytes, esp.Size)
	}
	temporary, err := layout.temporarySpace()
	if err != nil {
		return nil, err
	}
	if temporary > b.options.TemporarySpace {
		return nil, fmt.Errorf("%w: %w: split WIM needs %d temporary bytes", ErrNoSpace, ErrTemporarySpace, temporary)
	}
	sourceSHA256, err := b.hashSource()
	if err != nil {
		return nil, err
	}
	return &BuildPlan{sourceIdentity: b.options.SourceIdentity, sourceSHA256: sourceSHA256, manifest: installeriso.Manifest{Entries: b.entries}, architecture: UEFIX64,
		esp: esp, fat: fat, regularBytes: inspection.regular, strategy: layout.strategy, splitSize: uint64(b.splitSize), splitParts: layout.splitParts,
		temporaryBytes: temporary, verification: inspection.verification, planned: planned}, nil
}

// espLayout places the ESP after the 1 MiB alignment gap and leaves the last
// 33 sectors for the backup GPT.
func (b *planBuilder) espLayout() (ESPLayout, error) {
	reserved := partitionStart + 33*logicalSectorSize
	if b.options.TargetSize <= reserved {
		return ESPLayout{}, errors.Join(ErrNoSpace, ErrTargetTooSmall)
	}
	gptBytes := uint64(67) * logicalSectorSize // protective MBR, both headers, and both entry arrays
	return ESPLayout{LogicalSectorSize: logicalSectorSize, StartOffset: partitionStart, Size: b.options.TargetSize - reserved, GPTMetadataBytes: gptBytes}, nil
}

func approveDestinations(entries []installeriso.Entry) ([]plannedEntry, error) {
	approved := make([]plannedEntry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		d, err := approveDestination(e)
		if err != nil {
			return nil, err
		}
		key := d.foldKey()
		if seen[key] {
			return nil, fmt.Errorf("%w: destination collision %q", ErrUnsupported, d)
		}
		seen[key] = true
		approved = append(approved, plannedEntry{source: e, destination: string(d)})
	}
	return approved, nil
}

func approveDestination(entry installeriso.Entry) (destination, error) {
	d := destinationOf(entry)
	if d.unsafe() {
		return "", fmt.Errorf("%w: unsafe destination path %q", ErrUnsupported, d)
	}
	return d, nil
}

// destination is a slash-separated path on the ESP, relative to its root.
type destination string

func destinationOf(entry installeriso.Entry) destination {
	d := entry.DestinationFATPath
	if d == "" {
		d = entry.Path
	}
	return destination(strings.ReplaceAll(d, "\\", "/"))
}

func (d destination) clean() destination { return destination(path.Clean(string(d))) }

func (d destination) unsafe() bool {
	if d.nonCanonical() || d.clean().escapesRoot() {
		return true
	}
	return strings.ContainsRune(string(d), 0)
}

// nonCanonical reports empty, absolute, or not already cleaned paths.
func (d destination) nonCanonical() bool {
	if d == "" || path.IsAbs(string(d)) {
		return true
	}
	return d.clean() != d
}

func (d destination) escapesRoot() bool {
	if d == "." || d == ".." {
		return true
	}
	return strings.HasPrefix(string(d), "../")
}

// foldKey is the name FAT32 sees once case and Unicode normalization are
// folded away.
func (d destination) foldKey() string { return norm.NFC.String(cases.Fold().String(string(d))) }

func selectInstallStrategy(files manifestFiles, policy splitPolicy) (installLayout, error) {
	swm, err := files.existingSWM()
	if err != nil {
		return installLayout{}, err
	}
	if len(swm) != 0 {
		return files.existingSWMLayout(swm)
	}
	return files.singleImageLayout(policy)
}

// swmSet maps each existing SWM part's sequence number to its entry.
type swmSet map[int]installeriso.Entry

func (s swmSet) contiguous() bool {
	for i := 1; i <= len(s); i++ {
		if _, ok := s[i]; !ok {
			return false
		}
	}
	return true
}

func (f manifestFiles) existingSWM() (swmSet, error) {
	swm := swmSet{}
	for key, e := range f {
		if !key.looksLikeSWM() {
			continue
		}
		n, ok := key.swmSequence()
		if !ok {
			return nil, fmt.Errorf("%w: invalid existing SWM name %q", ErrUnsupported, key)
		}
		if _, duplicate := swm[n]; duplicate {
			return nil, fmt.Errorf("%w: duplicate existing SWM sequence %d", ErrUnsupported, n)
		}
		swm[n] = e
	}
	return swm, nil
}

func (f manifestFiles) existingSWMLayout(swm swmSet) (installLayout, error) {
	if f.has(keyInstallWIM) || f.has(keyInstallESD) {
		return installLayout{}, fmt.Errorf("%w: existing SWM set cannot be mixed with WIM or ESD", ErrUnsupported)
	}
	if !swm.contiguous() {
		return installLayout{}, fmt.Errorf("%w: existing SWM set is not contiguous", ErrUnsupported)
	}
	return installLayout{strategy: CopyExistingSWM, splitParts: len(swm)}, nil
}

func (f manifestFiles) singleImageLayout(policy splitPolicy) (installLayout, error) {
	wim, hasWIM := f[keyInstallWIM]
	esd, hasESD := f[keyInstallESD]
	if hasWIM && hasESD {
		return installLayout{}, fmt.Errorf("%w: multiple install images", ErrUnsupported)
	}
	if hasWIM {
		return policy.wimLayout(wim), nil
	}
	if !hasESD {
		return installLayout{strategy: NoInstallImage}, nil
	}
	if esd.Size > maxFATFileSize {
		return installLayout{}, fmt.Errorf("%w: install.esd exceeds FAT32 file limit", ErrUnsupported)
	}
	return installLayout{strategy: CopyESD, installSize: esd.Size, splitParts: 1}, nil
}

// installLayout describes how the install image will be written to the ESP.
type installLayout struct {
	strategy    InstallStrategy
	installSize uint64
	splitParts  int
}

// estimatedSplitClusters is the allocation for cutting the install image into
// splitParts pieces of at most policy bytes, before the real part sizes are
// known.
func (l installLayout) estimatedSplitClusters(policy splitPolicy, cluster clusterSize) (clusters uint64) {
	remaining := l.installSize
	for range l.splitParts {
		size := min(remaining, uint64(policy))
		clusters += cluster.clusters(size)
		remaining -= size
	}
	return clusters
}

func (l installLayout) temporarySpace() (uint64, error) {
	if l.strategy != SplitWIM {
		return 0, nil
	}
	return l.splitTemporarySpace()
}

// splitTemporarySpace reserves the simultaneously retained staged WIM, the
// maximum accepted SWM output set, and conservative filesystem metadata.
func (l installLayout) splitTemporarySpace() (uint64, error) {
	sourceSize, parts := l.installSize, l.splitParts
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

// directorySizes accumulates the directory-entry bytes each ESP directory
// needs, keyed by the directory's manifestKey; the root is "".
type directorySizes map[manifestKey]uint64

func newDirectorySizes() directorySizes { return directorySizes{"": 96} }

func (d directorySizes) addDirectory(entry installeriso.Entry) {
	d[keyOf(entry)] += 64
	d.addChild(entry)
}

func (d directorySizes) addChild(entry installeriso.Entry) {
	d[keyOf(entry).parent()] += fatName(path.Base(entry.Path)).directoryEntryBytes()
}

func (d directorySizes) total(cluster clusterSize) (bytes, clusters uint64) {
	for _, n := range d {
		bytes += n
		clusters += cluster.clusters(n)
	}
	return bytes, clusters
}

func (b *planBuilder) inspectFiles(cluster clusterSize, layout installLayout) (fileInspection, error) {
	var result fileInspection
	dirs := newDirectorySizes()
	for _, entry := range b.entries {
		if entry.Type == installeriso.Directory {
			dirs.addDirectory(entry)
			continue
		}
		if entry.Type != installeriso.File {
			continue
		}
		if err := validateFATFileSize(entry, layout.strategy); err != nil {
			return fileInspection{}, err
		}
		if !keyOf(entry).isInstallImage() {
			result.regular += entry.Size
		}
		result.fileClusters += b.entryClusters(entry, cluster, layout)
		dirs.addChild(entry)
		hash, err := b.hashExtents(entry)
		if err != nil {
			return fileInspection{}, err
		}
		result.verification = append(result.verification, VerificationEntry{verificationDestination(entry), entry.Size, hash})
	}
	result.directoryBytes, result.directoryClusters = dirs.total(cluster)
	return result, nil
}

func validateFATFileSize(entry installeriso.Entry, strategy InstallStrategy) error {
	if entry.Size <= maxFATFileSize {
		return nil
	}
	if strategy == SplitWIM && strings.EqualFold(entry.Path, string(keyInstallWIM)) {
		return nil
	}
	return fmt.Errorf("%w: %s exceeds FAT32 file limit", ErrUnsupported, entry.Path)
}

func (b *planBuilder) entryClusters(entry installeriso.Entry, cluster clusterSize, layout installLayout) uint64 {
	if layout.strategy != SplitWIM || keyOf(entry) != keyInstallWIM {
		return cluster.clusters(entry.Size)
	}
	return layout.estimatedSplitClusters(b.splitSize, cluster)
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

// hashSource digests the whole source image as one extent.
func (b *planBuilder) hashSource() (string, error) {
	whole := installeriso.Entry{Path: "source image", Size: b.sourceSize, Extents: []installeriso.Extent{{Offset: 0, Length: b.sourceSize}}}
	return b.hashExtents(whole)
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
		if err := h.add(installeriso.Extent{Offset: extent.Offset, Length: n}); err != nil {
			return "", err
		}
		remaining -= n
	}
	if remaining != 0 {
		return "", fmt.Errorf("%w: incomplete extent for %s", ErrUnsupported, entry.Path)
	}
	return hex.EncodeToString(h.digest.Sum(nil)), nil
}

func (h *extentHasher) add(x installeriso.Extent) error {
	if x.Offset > h.builder.sourceSize || x.Length > h.builder.sourceSize-x.Offset {
		return fmt.Errorf("%w: extent out of bounds", ErrUnsupported)
	}
	done := uint64(0)
	for done < x.Length {
		if err := h.builder.ctx.Err(); err != nil {
			return err
		}
		take := min(uint64(len(h.buffer)), x.Length-done)
		if _, err := h.builder.source.ReadAt(h.buffer[:take], int64(x.Offset+done)); err != nil {
			return err
		}
		_, _ = h.digest.Write(h.buffer[:take])
		done += take
	}
	return nil
}

func cloneManifest(m installeriso.Manifest) installeriso.Manifest {
	out := installeriso.Manifest{Entries: make([]installeriso.Entry, len(m.Entries))}
	for i, e := range m.Entries {
		out.Entries[i] = e
		out.Entries[i].Extents = append([]installeriso.Extent(nil), e.Extents...)
	}
	return out
}

func ceilDiv(n, d uint64) uint64 {
	if n == 0 {
		return 0
	}
	return 1 + (n-1)/d
}
