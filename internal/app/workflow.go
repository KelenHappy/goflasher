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
		if info.UncompressedSize > target.Size {
			return WorkflowPlan{}, writer.ErrTargetTooSmall
		}
		if err := info.VerifySourceContext(ctx); err != nil {
			return WorkflowPlan{}, err
		}
		return WorkflowPlan{Kind: PlanRawWrite}, nil
	case image.WindowsInstallerISO:
		if info.Compression != image.CompressionNone {
			return WorkflowPlan{}, ErrCompressedWindowsInstallerUnsupported
		}
		if !p.WindowsSupported {
			return WorkflowPlan{}, ErrInstallerBuilderUnavailable
		}
		// Availability is a property of the complete installer builder, not only
		// of plans that happen to need splitting. A package missing its pinned
		// native library must never advertise or execute even a copy-WIM plan.
		if p.SplitPreflight == nil {
			return WorkflowPlan{}, ErrInstallerBuilderUnavailable
		}
		if err := p.SplitPreflight(ctx); err != nil {
			return WorkflowPlan{}, errors.Join(ErrInstallerBuilderUnavailable, err)
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
		plan, err := installer.NewBuildPlan(ctx, r, uint64(size), fs.Manifest(), installer.PlanOptions{
			SourceIdentity: "retained:" + info.SHA256, TargetSize: target.Size,
			TemporarySpace: p.TemporarySpace, SplitPreflight: p.SplitPreflight,
		})
		if err != nil {
			return WorkflowPlan{}, err
		}
		return WorkflowPlan{Kind: PlanWindowsInstaller, Windows: plan}, nil
	default:
		return WorkflowPlan{}, image.ErrUnsafeClassification
	}
}

type InstallerTarget = device.InstallerTarget
type InstallerReader = device.InstallerReader
type WindowsInstallerBackend = device.WindowsInstallerBackend

type RawWriteExecutor struct{ service *Service }

func (x RawWriteExecutor) Execute(ctx context.Context, info image.Info, target device.Device, opts RunOptions, updates chan<- progress.Update, out *RunResult) error {
	if err := x.service.writeImage(ctx, info, target, updates, out); err != nil {
		return err
	}
	if err := x.service.verifyImage(ctx, target, opts.Verify, updates, out); err != nil {
		return err
	}
	return nil
}

type WindowsInstallerExecutor struct {
	backend  WindowsInstallerBackend
	splitter installer.WIMSplitter
	state    *StateMachine
}

func (x WindowsInstallerExecutor) Execute(ctx context.Context, plan *installer.BuildPlan, info image.Info, target device.Device, updates chan<- progress.Update, out *RunResult) (err error) {
	r, _, _, err := info.RetainedReaderAt()
	if err != nil {
		return err
	}
	if err := x.state.Transition(Partitioning); err != nil {
		return err
	}
	sendStage(ctx, updates, progress.StagePartitioning)
	raw, err := x.backend.OpenInstallerTarget(ctx, target)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, raw.Close()) }()
	if err := x.state.Transition(Formatting); err != nil {
		return err
	}
	sendStage(ctx, updates, progress.StageFormatting)
	if err := x.state.Transition(Extracting); err != nil {
		return err
	}
	sendStage(ctx, updates, progress.StageExtracting)
	result, err := (installer.Executor{Splitter: x.splitter}).Execute(ctx, plan, r, raw)
	if err != nil {
		return err
	}
	if !result.Complete {
		return installer.ErrIncomplete
	}
	out.FilesWritten = len(result.VerificationManifest)
	out.ManifestSHA256 = verificationManifestHash(result.VerificationManifest)
	out.installerManifest = append([]installer.VerificationEntry(nil), result.VerificationManifest...)
	for _, entry := range result.VerificationManifest {
		if entry.Path == "sources/install.swm" || strings.HasPrefix(entry.Path, "sources/install") && strings.HasSuffix(entry.Path, ".swm") {
			out.WIMParts++
		}
	}
	return nil
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
