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

func TestBuildPlanIsCompleteAndImmutable(t *testing.T) {
	source := bytes.Repeat([]byte{0x5a}, 8192)
	manifest := installeriso.Manifest{Entries: []installeriso.Entry{
		{Path: "efi", Type: installeriso.Directory},
		{Path: "efi/boot", Type: installeriso.Directory},
		{Path: "efi/boot/bootx64.efi", DestinationFATPath: "efi/boot/bootx64.efi", Type: installeriso.File, Size: 1000, Extents: []installeriso.Extent{{Offset: 0, Length: 1000}}},
		{Path: "sources/install.wim", DestinationFATPath: "sources/install.wim", Type: installeriso.File, Size: 4096, Extents: []installeriso.Extent{{Offset: 4096, Length: 4096}}},
	}}
	plan, err := NewBuildPlan(context.Background(), bytes.NewReader(source), uint64(len(source)), manifest, PlanOptions{SourceIdentity: "iso:fixture", TargetSize: 8 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Architecture() != UEFIX64 || plan.InstallStrategy() != CopyWIM || plan.SourceSHA256() == "" {
		t.Fatalf("incomplete plan: %#v", plan)
	}
	if plan.FATLayoutEstimate().FileClusters != 2 || plan.FATLayoutEstimate().FATBytes == 0 || plan.ESPLayout().GPTMetadataBytes == 0 {
		t.Fatalf("invalid allocation estimate: %+v", plan.FATLayoutEstimate())
	}
	copy := plan.Manifest()
	copy.Entries[0].Path = "changed"
	if plan.Manifest().Entries[0].Path == "changed" {
		t.Fatal("manifest accessor mutated the build plan")
	}
	verification := plan.VerificationManifest()
	verification[0].Path = "changed"
	if plan.VerificationManifest()[0].Path == "changed" {
		t.Fatal("verification accessor mutated the build plan")
	}
}

func TestBuildPlanRequiresX64FallbackLoader(t *testing.T) {
	_, err := NewBuildPlan(context.Background(), bytes.NewReader(make([]byte, 512)), 512, installeriso.Manifest{}, PlanOptions{SourceIdentity: "iso", TargetSize: 1 << 30})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildPlanValidatesExistingSplitSet(t *testing.T) {
	entries := []installeriso.Entry{{Path: "efi/boot/bootx64.efi", Type: installeriso.File}, {Path: "sources/install.swm", Type: installeriso.File}, {Path: "sources/install3.swm", Type: installeriso.File}}
	_, err := NewBuildPlan(context.Background(), bytes.NewReader(make([]byte, 512)), 512, installeriso.Manifest{Entries: entries}, PlanOptions{SourceIdentity: "iso", TargetSize: 1 << 30})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("non-contiguous split set error = %v", err)
	}
}

func TestSplitWIMAccountsForAllocationAndTemporarySpace(t *testing.T) {
	files := map[string]installeriso.Entry{"sources/install.wim": {Size: maxFATFileSize + 1}}
	strategy, size, parts, err := selectInstallStrategy(files, defaultSplitSize)
	if err != nil || strategy != SplitWIM || size != maxFATFileSize+1 || parts != 2 {
		t.Fatalf("strategy = %s, size=%d, parts=%d, err=%v", strategy, size, parts, err)
	}
	cluster := uint64(4096)
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

func TestBuildPlanProbesSplitSupportBeforeCompletingPreflight(t *testing.T) {
	probeErr := errors.New("bundled libwim unavailable")
	manifest := installeriso.Manifest{Entries: []installeriso.Entry{
		{Path: "efi/boot/bootx64.efi", Type: installeriso.File},
		{Path: "sources/install.wim", Type: installeriso.File, Size: maxFATFileSize + 1},
	}}
	_, err := NewBuildPlan(context.Background(), bytes.NewReader(make([]byte, 512)), 512, manifest, PlanOptions{
		SourceIdentity: "iso", TargetSize: 8 << 30, TemporarySpace: 8 << 30,
		SplitPreflight: func(context.Context) error { return probeErr },
	})
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), probeErr.Error()) {
		t.Fatalf("error=%v", err)
	}
}
