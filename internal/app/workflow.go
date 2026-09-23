package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/image"
	"github.com/goflasher/goflasher/internal/installer"
	installeriso "github.com/goflasher/goflasher/internal/installer/iso"
	"github.com/goflasher/goflasher/internal/progress"
	"github.com/goflasher/goflasher/internal/writer"
)

type PlanKind string

const (
	PlanRawWrite         PlanKind = "raw-write"
	PlanWindowsInstaller PlanKind = "windows-installer"
)

type WorkflowPlan struct {
	Kind    PlanKind
	Windows *installer.BuildPlan
}

type WorkflowPlanner struct {
	TemporarySpace   uint64
	SplitPreflight   func(context.Context) error
	WindowsSupported bool
}

func (p WorkflowPlanner) Plan(ctx context.Context, info image.Info, target device.Device) (WorkflowPlan, error) {
	kind, err := image.ClassifyContext(ctx, info)
	if err != nil {
		return WorkflowPlan{}, err
	}
	switch kind {
	case image.RawDiskImage, image.LinuxHybridISO:
		return planRawWrite(ctx, info, target)
	case image.WindowsInstallerISO:
		return p.planWindowsInstaller(ctx, info, target)
	default:
		return WorkflowPlan{}, image.ErrUnsafeClassification
	}
}

func planRawWrite(ctx context.Context, info image.Info, target device.Device) (WorkflowPlan, error) {
	if info.UncompressedSize > target.Size {
		return WorkflowPlan{}, writer.ErrTargetTooSmall
	}
	if err := info.VerifySourceContext(ctx); err != nil {
		return WorkflowPlan{}, err
	}
	return WorkflowPlan{Kind: PlanRawWrite}, nil
}

func (p WorkflowPlanner) planWindowsInstaller(ctx context.Context, info image.Info, target device.Device) (WorkflowPlan, error) {
	if err := p.validateWindowsInstaller(ctx, info); err != nil {
		return WorkflowPlan{}, err
	}
	r, size, lease, err := info.RetainedReaderAt()
	if err != nil {
		return WorkflowPlan{}, err
	}
	fs, err := installeriso.New(r, size, lease)
	if err != nil {
		return WorkflowPlan{}, err
	}
	defer fs.Close()
	plan, err := installer.NewBuildPlan(ctx, installer.BuildPlanInput{
		Source: r, SourceSize: uint64(size), Manifest: fs.Manifest(),
		Options: installer.PlanOptions{
			SourceIdentity: "retained:" + info.SHA256, TargetSize: target.Size,
			TemporarySpace: p.TemporarySpace, SplitPreflight: p.SplitPreflight,
		},
	})
	if err != nil {
		return WorkflowPlan{}, err
	}
	return WorkflowPlan{Kind: PlanWindowsInstaller, Windows: plan}, nil
}

func (p WorkflowPlanner) validateWindowsInstaller(ctx context.Context, info image.Info) error {
	if info.Compression != image.CompressionNone {
		return ErrCompressedWindowsInstallerUnsupported
	}
	// Availability is a property of the complete installer builder, not only
	// of plans that happen to need splitting. A package missing its pinned
	// native library must never advertise or execute even a copy-WIM plan.
	if !p.WindowsSupported || p.SplitPreflight == nil {
		return ErrInstallerBuilderUnavailable
	}
	if err := p.SplitPreflight(ctx); err != nil {
		return errors.Join(ErrInstallerBuilderUnavailable, err)
	}
	return nil
}

type InstallerTarget = device.InstallerTarget
type InstallerReader = device.InstallerReader
type WindowsInstallerBackend = device.WindowsInstallerBackend

type RawWriteExecutor struct{ service *Service }

type rawWriteRequest struct {
	operation workflowOperation
	info      image.Info
	options   RunOptions
}

func (x RawWriteExecutor) Execute(request rawWriteRequest) error {
	request.operation.service = x.service
	if err := request.operation.writeImage(request.info); err != nil {
		return err
	}
	if err := request.operation.verifyImage(request.options.Verify); err != nil {
		return err
	}
	return nil
}

type WindowsInstallerExecutor struct {
	backend  WindowsInstallerBackend
	splitter installer.WIMSplitter
	state    *StateMachine
}

// installerRequest is one Windows installer build: what to write, where, and
// where to report progress and results.
type installerRequest struct {
	plan    *installer.BuildPlan
	info    image.Info
	target  device.Device
	updates chan<- progress.Update
	out     *RunResult
}

func (x WindowsInstallerExecutor) Execute(ctx context.Context, req installerRequest) (err error) {
	r, _, _, err := req.info.RetainedReaderAt()
	if err != nil {
		return err
	}
	if err := x.enter(ctx, req.updates, Partitioning, progress.StagePartitioning); err != nil {
		return err
	}
	raw, err := x.backend.OpenInstallerTarget(ctx, req.target)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, raw.Close()) }()
	if err := x.enter(ctx, req.updates, Formatting, progress.StageFormatting); err != nil {
		return err
	}
	if err := x.enter(ctx, req.updates, Extracting, progress.StageExtracting); err != nil {
		return err
	}
	result, err := (installer.Executor{Splitter: x.splitter}).Execute(ctx, req.plan, r, raw)
	if err != nil {
		return err
	}
	if !result.Complete {
		return installer.ErrIncomplete
	}
	req.out.recordInstallerManifest(result.VerificationManifest)
	return nil
}

// enter advances the state machine and reports the stage that goes with it.
func (x WindowsInstallerExecutor) enter(ctx context.Context, updates chan<- progress.Update, next State, stage progress.Stage) error {
	if err := x.state.Transition(next); err != nil {
		return err
	}
	sendStage(ctx, updates, stage)
	return nil
}

// recordInstallerManifest stores the build's verification manifest on the
// result and counts the split WIM parts it contains.
func (out *RunResult) recordInstallerManifest(entries []installer.VerificationEntry) {
	out.FilesWritten = len(entries)
	out.ManifestSHA256 = verificationManifestHash(entries)
	out.installerManifest = append([]installer.VerificationEntry(nil), entries...)
	for _, entry := range entries {
		if isWIMPart(entry.Path) {
			out.WIMParts++
		}
	}
}

// isWIMPart reports whether path is one of the sources/install*.swm parts that
// splitting an oversized install.wim produces.
func isWIMPart(path string) bool {
	return strings.HasPrefix(path, "sources/install") && strings.HasSuffix(path, ".swm")
}

func verificationManifestHash(entries []installer.VerificationEntry) string {
	copy := append([]installer.VerificationEntry(nil), entries...)
	sort.Slice(copy, func(i, j int) bool { return copy[i].Path < copy[j].Path })
	h := sha256.New()
	for _, e := range copy {
		fmt.Fprintf(h, "%s\x00%d\x00%s\n", e.Path, e.Size, e.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}
