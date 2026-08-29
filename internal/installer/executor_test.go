package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	installeriso "github.com/goflasher/goflasher/internal/installer/iso"
)

type sparseTarget struct {
	size   uint64
	writes uint64
	syncs  int
}

func (d *sparseTarget) WriteAt(p []byte, off int64) (int, error) {
	if !d.holds(off, len(p)) {
		return 0, io.ErrShortWrite
	}
	d.writes += uint64(len(p))
	return len(p), nil
}

// holds models a fixed-size device: a write must start inside the target and
// end within it.
func (d *sparseTarget) holds(off int64, n int) bool {
	if off < 0 || uint64(off) > d.size {
		return false
	}
	return uint64(n) <= d.size-uint64(off)
}
func (d *sparseTarget) Sync() error { d.syncs++; return nil }

func executorFixture(t *testing.T) (*BuildPlan, []byte) {
	t.Helper()
	source := append(bytes.Repeat([]byte{0x42}, 1000), bytes.Repeat([]byte{0x77}, 3000)...)
	manifest := installeriso.Manifest{Entries: []installeriso.Entry{
		{Path: "efi", DestinationFATPath: "efi", Type: installeriso.Directory},
		{Path: "efi/boot", DestinationFATPath: "efi\\boot", Type: installeriso.Directory},
		{Path: "efi/boot/bootx64.efi", DestinationFATPath: "efi\\boot\\bootx64.efi", Type: installeriso.File, Size: 1000, Extents: []installeriso.Extent{{Offset: 0, Length: 1000}}},
		{Path: "sources", DestinationFATPath: "sources", Type: installeriso.Directory},
		{Path: "sources/install.wim", DestinationFATPath: "sources\\install.wim", Type: installeriso.File, Size: 3000, Extents: []installeriso.Extent{{Offset: 1000, Length: 3000}}},
	}}
	plan, err := NewBuildPlan(context.Background(), BuildPlanInput{Source: bytes.NewReader(source), SourceSize: uint64(len(source)), Manifest: manifest, Options: PlanOptions{SourceIdentity: "fixture", TargetSize: 80 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	return plan, source
}

func TestExecutorStreamsAndVerifiesEveryCopiedFile(t *testing.T) {
	plan, source := executorFixture(t)
	target := &sparseTarget{size: 80 << 20}
	result, err := (Executor{}).Execute(context.Background(), plan, bytes.NewReader(source), target)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete {
		t.Errorf("result is incomplete: %+v", result)
	}
	if result.BytesWritten != 4000 {
		t.Errorf("bytes written = %d, want 4000", result.BytesWritten)
	}
	if len(result.VerificationManifest) != 2 {
		t.Errorf("verified files = %d, want 2", len(result.VerificationManifest))
	}
	if target.syncs == 0 {
		t.Error("executor never synced the target")
	}
}

func TestExecutorHashMismatchIsIncomplete(t *testing.T) {
	plan, source := executorFixture(t)
	source[0] ^= 0xff
	result, err := (Executor{}).Execute(context.Background(), plan, bytes.NewReader(source), &sparseTarget{size: 80 << 20})
	if !errors.Is(err, ErrVerification) || result.Complete {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

type fixtureSplitter struct{}

func (fixtureSplitter) Split(_ context.Context, source io.Reader, size uint64, _ string, _ uint64, emit func(SplitPart) error) error {
	b, err := io.ReadAll(source)
	if err != nil {
		return err
	}
	if uint64(len(b)) != size {
		return io.ErrUnexpectedEOF
	}
	return emit(SplitPart{Name: "install.swm", Size: uint64(len(b)), Data: bytes.NewReader(b)})
}

func TestExecutorSkipsOriginalWIMAndWaitsForSplitParts(t *testing.T) {
	plan, source := executorFixture(t)
	plan.strategy = SplitWIM
	plan.splitSize = 4096
	target := &sparseTarget{size: 80 << 20}
	result, err := (Executor{Splitter: fixtureSplitter{}}).Execute(context.Background(), plan, bytes.NewReader(source), target)
	if err != nil || !result.Complete {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	for _, entry := range result.VerificationManifest {
		if entry.Path == "sources/install.wim" {
			t.Fatal("executor created the original install.wim")
		}
	}
	if got := result.VerificationManifest[len(result.VerificationManifest)-1].Path; got != "sources/install.swm" {
		t.Fatalf("split destination = %q", got)
	}
}

func TestExecutorRequiresSplitPipelineBeforeFirstTargetWrite(t *testing.T) {
	plan, source := executorFixture(t)
	plan.strategy = SplitWIM
	target := &sparseTarget{size: 80 << 20}
	result, err := (Executor{}).Execute(context.Background(), plan, bytes.NewReader(source), target)
	if !errors.Is(err, ErrSplitterRequired) {
		t.Errorf("error = %v, want %v", err, ErrSplitterRequired)
	}
	if result.Complete {
		t.Errorf("result reports complete: %+v", result)
	}
	if target.writes != 0 {
		t.Errorf("target writes = %d, want none before the splitter is checked", target.writes)
	}
}

func TestValidateSplitPartRejectsInvalidParts(t *testing.T) {
	valid := SplitPart{Name: "install.swm", Size: 1, Data: bytes.NewReader([]byte{1})}
	tests := []struct {
		name  string
		part  SplitPart
		index int
	}{
		{name: "too many", part: valid, index: 2},
		{name: "wrong name", part: SplitPart{Name: "wrong.swm", Size: 1, Data: valid.Data}, index: 1},
		{name: "nil data", part: SplitPart{Name: "install.swm", Size: 1}, index: 1},
		{name: "zero size", part: SplitPart{Name: "install.swm", Data: valid.Data}, index: 1},
		{name: "oversized", part: SplitPart{Name: "install.swm", Size: 2, Data: valid.Data}, index: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateSplitPart(test.part, test.index, 1, 1); !errors.Is(err, ErrVerification) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestExecutorRejectsFewerSplitPartsThanPlanned(t *testing.T) {
	plan, source := executorFixture(t)
	plan.strategy = SplitWIM
	plan.splitSize = 4096
	plan.splitParts = 2
	result, err := (Executor{Splitter: fixtureSplitter{}}).Execute(context.Background(), plan, bytes.NewReader(source), &sparseTarget{size: 80 << 20})
	const want = "split pipeline produced 1 of 2 planned parts"
	if err == nil {
		t.Fatalf("short split set accepted: result=%+v", result)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want it to report %q", err, want)
	}
	if result.Complete {
		t.Errorf("result reports complete: %+v", result)
	}
}

func TestExecutorCancellationNeverCompletes(t *testing.T) {
	plan, source := executorFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := (Executor{}).Execute(ctx, plan, bytes.NewReader(source), &sparseTarget{size: 80 << 20})
	if !errors.Is(err, context.Canceled) || result.Complete {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
