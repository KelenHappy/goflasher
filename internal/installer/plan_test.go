package installer

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	installeriso "github.com/goflasher/goflasher/internal/installer/iso"
)

func newCopyWIMPlan(t *testing.T) *BuildPlan {
	t.Helper()
	source := bytes.Repeat([]byte{0x5a}, 8192)
	manifest := installeriso.Manifest{Entries: []installeriso.Entry{
		{Path: "efi", Type: installeriso.Directory},
		{Path: "efi/boot", Type: installeriso.Directory},
		{Path: "efi/boot/bootx64.efi", DestinationFATPath: "efi/boot/bootx64.efi", Type: installeriso.File, Size: 1000, Extents: []installeriso.Extent{{Offset: 0, Length: 1000}}},
		{Path: "sources/install.wim", DestinationFATPath: "sources/install.wim", Type: installeriso.File, Size: 4096, Extents: []installeriso.Extent{{Offset: 4096, Length: 4096}}},
	}}
	plan, err := NewBuildPlan(context.Background(), BuildPlanInput{Source: bytes.NewReader(source), SourceSize: uint64(len(source)), Manifest: manifest, Options: PlanOptions{SourceIdentity: "iso:fixture", TargetSize: 8 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestBuildPlanIsComplete(t *testing.T) {
	plan := newCopyWIMPlan(t)
	if got := plan.Architecture(); got != UEFIX64 {
		t.Errorf("architecture = %v, want %v", got, UEFIX64)
	}
	if got := plan.InstallStrategy(); got != CopyWIM {
		t.Errorf("strategy = %v, want %v", got, CopyWIM)
	}
	if plan.SourceSHA256() == "" {
		t.Error("source digest is empty")
	}
}

func TestBuildPlanEstimatesAllocation(t *testing.T) {
	plan := newCopyWIMPlan(t)
	fat := plan.FATLayoutEstimate()
	if fat.FileClusters != 2 {
		t.Errorf("file clusters = %d, want 2", fat.FileClusters)
	}
	if fat.FATBytes == 0 {
		t.Errorf("FAT bytes = 0, want a sized allocation table: %+v", fat)
	}
	if esp := plan.ESPLayout(); esp.GPTMetadataBytes == 0 {
		t.Errorf("GPT metadata bytes = 0, want reserved partition metadata: %+v", esp)
	}
}

func TestBuildPlanManifestAccessorIsImmutable(t *testing.T) {
	plan := newCopyWIMPlan(t)
	snapshot := plan.Manifest()
	snapshot.Entries[0].Path = "changed"
	if plan.Manifest().Entries[0].Path == "changed" {
		t.Fatal("manifest accessor mutated the build plan")
	}
}

func TestBuildPlanVerificationAccessorIsImmutable(t *testing.T) {
	plan := newCopyWIMPlan(t)
	verification := plan.VerificationManifest()
	verification[0].Path = "changed"
	if plan.VerificationManifest()[0].Path == "changed" {
		t.Fatal("verification accessor mutated the build plan")
	}
}

func TestBuildPlanRequiresX64FallbackLoader(t *testing.T) {
	_, err := NewBuildPlan(context.Background(), BuildPlanInput{Source: bytes.NewReader(make([]byte, 512)), SourceSize: 512, Manifest: installeriso.Manifest{}, Options: PlanOptions{SourceIdentity: "iso", TargetSize: 1 << 30}})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildPlanValidatesExistingSplitSet(t *testing.T) {
	entries := []installeriso.Entry{{Path: "efi/boot/bootx64.efi", Type: installeriso.File}, {Path: "sources/install.swm", Type: installeriso.File}, {Path: "sources/install3.swm", Type: installeriso.File}}
	_, err := NewBuildPlan(context.Background(), BuildPlanInput{Source: bytes.NewReader(make([]byte, 512)), SourceSize: 512, Manifest: installeriso.Manifest{Entries: entries}, Options: PlanOptions{SourceIdentity: "iso", TargetSize: 1 << 30}})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("non-contiguous split set error = %v", err)
	}
}

func TestSplitWIMStrategySizesOversizedInstallWIM(t *testing.T) {
	files := map[string]installeriso.Entry{"sources/install.wim": {Size: maxFATFileSize + 1}}
	strategy, size, parts, err := selectInstallStrategy(files, defaultSplitSize)
	if err != nil {
		t.Fatal(err)
	}
	if strategy != SplitWIM {
		t.Errorf("strategy = %s, want %s", strategy, SplitWIM)
	}
	if size != maxFATFileSize+1 {
		t.Errorf("size = %d, want %d", size, maxFATFileSize+1)
	}
	if parts != 2 {
		t.Errorf("parts = %d, want 2", parts)
	}
}

func TestSplitWIMAccountsForAllocationAndTemporarySpace(t *testing.T) {
	const (
		size    = maxFATFileSize + 1
		parts   = 2
		cluster = uint64(4096)
	)
	allocated := ceilDiv(defaultSplitSize, cluster) + ceilDiv(size-defaultSplitSize, cluster)
	if allocated*cluster < size {
		t.Fatal("split allocation did not round each output to an allocation unit")
	}
	temporary, err := estimateSplitTemporarySpace(size, parts)
	if err != nil {
		t.Fatal(err)
	}
	if temporary <= size*2 {
		t.Fatalf("temporary requirement=%d, must retain staged WIM and complete split set plus overhead", temporary)
	}
}

func TestSplitTemporarySpaceIncludesMaximumOutputAndFilesystemOverhead(t *testing.T) {
	const source = uint64(5 << 30)
	got, err := estimateSplitTemporarySpace(source, 2)
	if err != nil {
		t.Fatal(err)
	}
	maximumOutput := source + source/4 + 2*(1<<20)
	if got <= source+maximumOutput {
		t.Fatalf("temporary bytes=%d, staged+outputs=%d", got, source+maximumOutput)
	}
	if _, err := estimateSplitTemporarySpace(math.MaxUint64, 2); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("overflow error=%v", err)
	}
}

func TestPreparedSplitGeometryReplacesSourceSizeEstimate(t *testing.T) {
	plan := &BuildPlan{
		strategy: SplitWIM, splitSize: 100, splitParts: 2,
		esp:     ESPLayout{Size: 1 << 20},
		fat:     FATLayoutEstimate{ClusterSize: 64, FileClusters: 4, DirectoryClusters: 1},
		planned: []plannedEntry{{source: installeriso.Entry{Path: "sources/install.wim", Size: 200}}},
	}
	finalized, err := plan.withPreparedSplitGeometry([]uint64{90, 90, 30})
	if err != nil {
		t.Fatal(err)
	}
	if finalized.splitParts != 3 {
		t.Fatalf("parts=%d, want actual libwim count 3", finalized.splitParts)
	}
	if finalized.fat.FileClusters != 5 {
		t.Fatalf("file clusters=%d, want allocation-unit-rounded actual parts", finalized.fat.FileClusters)
	}
	if plan.splitParts != 2 || plan.fat.FileClusters != 4 {
		t.Fatal("finalizing geometry mutated the preview plan")
	}
}

func TestBuildPlanProbesSplitSupportBeforeCompletingPreflight(t *testing.T) {
	probeErr := errors.New("bundled libwim unavailable")
	manifest := installeriso.Manifest{Entries: []installeriso.Entry{
		{Path: "efi/boot/bootx64.efi", Type: installeriso.File},
		{Path: "sources/install.wim", Type: installeriso.File, Size: maxFATFileSize + 1},
	}}
	_, err := NewBuildPlan(context.Background(), BuildPlanInput{Source: bytes.NewReader(make([]byte, 512)), SourceSize: 512, Manifest: manifest, Options: PlanOptions{
		SourceIdentity: "iso", TargetSize: 8 << 30, TemporarySpace: 8 << 30,
		SplitPreflight: func(context.Context) error { return probeErr },
	}})
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), probeErr.Error()) {
		t.Fatalf("error=%v", err)
	}
}
