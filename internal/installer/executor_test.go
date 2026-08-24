package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	installeriso "github.com/goflasher/goflasher/internal/installer/iso"
)

type sparseTarget struct {
	size   uint64
	writes uint64
	syncs  int
}

func (d *sparseTarget) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || uint64(off) > d.size || uint64(len(p)) > d.size-uint64(off) {
		return 0, io.ErrShortWrite
	}
	d.writes += uint64(len(p))
	return len(p), nil
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
	if !result.Complete || result.BytesWritten != 4000 || len(result.VerificationManifest) != 2 || target.syncs == 0 {
		t.Fatalf("result=%+v syncs=%d", result, target.syncs)
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
	if !errors.Is(err, ErrSplitterRequired) || result.Complete || target.writes != 0 {
		t.Fatalf("result=%+v error=%v target writes=%d", result, err, target.writes)
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
