package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/image"
	"github.com/goflasher/goflasher/internal/installer"
	"github.com/goflasher/goflasher/internal/progress"
	"github.com/goflasher/goflasher/internal/verify"
	"github.com/goflasher/goflasher/internal/writer"
)

// RunOptions controls optional post-write safety steps.
type RunOptions struct{ Verify, Eject bool }

// RunResult summarizes work completed by Service.Run. A failed run may return
// partial values, allowing callers to report how far destructive work got.
type RunResult struct {
	BytesWritten               uint64
	SourceSHA256, TargetSHA256 string
	Elapsed                    time.Duration
	AverageBytesPerSecond      float64
	Verified, Ejected          bool
	PlanKind                   PlanKind
	FilesWritten               int
	ManifestSHA256             string
	WIMParts                   int
	SemanticVerified           bool
	SemanticVerification       string
	installerManifest          []installer.VerificationEntry
}

// Service coordinates the device workflow and its state transitions.
type Service struct {
	Backend           device.Backend
	State             *StateMachine
	TemporarySpace    uint64
	InstallerSplitter installer.WIMSplitter
}

var ErrInstallerBuilderUnavailable = errors.New("FAT32 Windows installer builder is unavailable")
var ErrCompressedWindowsInstallerUnsupported = errors.New("compressed Windows installer ISO is unsupported")

// Run owns the destructive workflow. Safety checks are deliberately repeated
// by the backend immediately before opening the block device.
func (s *Service) Run(ctx context.Context, info image.Info, target device.Device, opts RunOptions, updates chan<- progress.Update) (RunResult, error) {
	start := time.Now()
	if s.State == nil {
		s.State = NewStateMachine()
	}
	if s.InstallerSplitter == nil {
		s.InstallerSplitter = installer.NewNativeWIMSplitter()
	}
	if s.TemporarySpace == 0 {
		s.TemporarySpace, _ = availableTemporarySpace()
	}
	out, err := s.runWorkflow(ctx, info, target, opts, updates)
	s.finishRun(&out, err, time.Since(start))
	return out, err
}

type ImageClass string

const (
	ImageLinuxRaw   ImageClass = "linux-raw"
	ImageWindowsISO ImageClass = "windows-installer"
)

// PlanSummary is the read-only configuration presented before confirmation.
type PlanSummary struct {
	Class, PartitionTable, Filesystem, BootMode string
	SplitRequired                               bool
	SplitReason                                 string
	RequiredCapacity, RequiredTemporarySpace    uint64
	AvailableTemporarySpace                     uint64
}

func (s *Service) Preview(ctx context.Context, info image.Info, target device.Device) (summary PlanSummary, err error) {
	if s.InstallerSplitter == nil {
		s.InstallerSplitter = installer.NewNativeWIMSplitter()
	}
	if s.TemporarySpace == 0 {
		s.TemporarySpace, _ = availableTemporarySpace()
	}
	info, err = inspectImage(ctx, info)
	if err != nil {
		return summary, err
	}
	defer func() { err = errors.Join(err, info.CloseSource()) }()
	var preflight func(context.Context) error
	if p, ok := s.InstallerSplitter.(interface{ Preflight(context.Context) error }); ok {
		preflight = p.Preflight
	}
	_, supported := s.Backend.(WindowsInstallerBackend)
	plan, err := (WorkflowPlanner{TemporarySpace: s.TemporarySpace, SplitPreflight: preflight, WindowsSupported: supported}).Plan(ctx, info, target)
	if err != nil {
		return summary, err
	}
	if plan.Kind == PlanRawWrite {
		return PlanSummary{Class: string(ImageLinuxRaw)}, nil
	}
	split := plan.Windows.InstallStrategy() == installer.SplitWIM
	reason := ""
	if split {
		reason = "install.wim exceeds the FAT32 single-file limit"
	}
	return PlanSummary{Class: string(ImageWindowsISO), PartitionTable: "GPT", Filesystem: "FAT32", BootMode: "UEFI x64 only", SplitRequired: split, SplitReason: reason,
		RequiredCapacity: plan.Windows.RequiredTargetCapacity(), RequiredTemporarySpace: plan.Windows.TemporarySpaceRequired(), AvailableTemporarySpace: s.TemporarySpace}, nil
}

func (s *Service) runWorkflow(ctx context.Context, info image.Info, target device.Device, opts RunOptions, updates chan<- progress.Update) (out RunResult, err error) {
	info, plan, err := s.inspectAndPlan(ctx, info, target, updates)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, info.CloseSource()) }()
	out.PlanKind = plan.Kind
	windowsBackend, effectiveSplitter, cleanup, err := s.prepareWindowsInstaller(ctx, info, &plan, updates)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	if err = s.unmountDevice(ctx, target); err != nil {
		return out, err
	}
	releaseDevice := newDeviceRelease(s.Backend, target)
	defer func() { err = errors.Join(err, releaseDevice()) }()
	if err = s.executePlan(ctx, plan, info, target, opts, updates, windowsBackend, effectiveSplitter, &out); err != nil {
		return out, err
	}
	if err = s.ejectDevice(ctx, target, opts.Eject, updates, &out); err != nil {
		return out, err
	}
	if err = releaseDevice(); err != nil {
		return out, err
	}
	if err = s.State.Transition(Completed); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Service) inspectAndPlan(ctx context.Context, info image.Info, target device.Device, updates chan<- progress.Update) (image.Info, WorkflowPlan, error) {
	trackState := s.State.State() != Idle
	if err := transitionWorkflowState(s.State, trackState, Inspecting); err != nil {
		return info, WorkflowPlan{}, err
	}
	sendStage(ctx, updates, progress.StageInspecting)
	inspected, err := inspectImage(ctx, info)
	if err != nil {
		return info, WorkflowPlan{}, err
	}
	if err := transitionWorkflowState(s.State, trackState, Planning); err != nil {
		return inspected, WorkflowPlan{}, errors.Join(err, inspected.CloseSource())
	}
	sendStage(ctx, updates, progress.StagePlanning)
	plan, err := s.workflowPlanner().Plan(ctx, inspected, target)
	if err != nil {
		return inspected, WorkflowPlan{}, errors.Join(err, inspected.CloseSource())
	}
	return inspected, plan, nil
}

func transitionWorkflowState(state *StateMachine, enabled bool, next State) error {
	if !enabled {
		return nil
	}
	return state.Transition(next)
}

func (s *Service) workflowPlanner() WorkflowPlanner {
	var splitPreflight func(context.Context) error
	if preflight, ok := s.InstallerSplitter.(interface{ Preflight(context.Context) error }); ok {
		splitPreflight = preflight.Preflight
	}
	_, windowsSupported := s.Backend.(WindowsInstallerBackend)
	return WorkflowPlanner{
		TemporarySpace:   s.TemporarySpace,
		SplitPreflight:   splitPreflight,
		WindowsSupported: windowsSupported,
	}
}

func (s *Service) prepareWindowsInstaller(ctx context.Context, info image.Info, plan *WorkflowPlan, updates chan<- progress.Update) (WindowsInstallerBackend, installer.WIMSplitter, func() error, error) {
	noCleanup := func() error { return nil }
	if plan.Kind != PlanWindowsInstaller {
		return nil, s.InstallerSplitter, noCleanup, nil
	}
	backend, ok := s.Backend.(WindowsInstallerBackend)
	if !ok {
		return nil, nil, noCleanup, ErrInstallerBuilderUnavailable
	}
	if plan.Windows.InstallStrategy() != installer.SplitWIM {
		return backend, s.InstallerSplitter, noCleanup, nil
	}
	if s.InstallerSplitter == nil {
		return nil, nil, noCleanup, ErrInstallerBuilderUnavailable
	}
	prepared, cleanup, err := s.prepareSplitWIM(ctx, info, plan, updates)
	if err != nil {
		return nil, nil, noCleanup, err
	}
	return backend, prepared, cleanup, nil
}

func (s *Service) prepareSplitWIM(ctx context.Context, info image.Info, plan *WorkflowPlan, updates chan<- progress.Update) (installer.WIMSplitter, func() error, error) {
	r, _, _, err := info.RetainedReaderAt()
	if err != nil {
		return nil, nil, err
	}
	if err := s.State.Transition(StagingWIM); err != nil {
		return nil, nil, err
	}
	sendStage(ctx, updates, progress.StageStagingWIM)
	finalized, prepared, cleanup, err := installer.PrepareSplitWIM(ctx, installer.SplitWIMPreparation{Plan: plan.Windows, Source: r, Splitter: s.InstallerSplitter, OnSplitting: func() error {
		return s.startWIMSplit(ctx, updates)
	}})
	if err != nil {
		return nil, nil, errors.Join(installer.ErrWIMSplitFailure, err)
	}
	plan.Windows = finalized
	return prepared, cleanup.Close, nil
}

func (s *Service) startWIMSplit(ctx context.Context, updates chan<- progress.Update) error {
	if err := s.State.Transition(SplittingWIM); err != nil {
		return err
	}
	sendStage(ctx, updates, progress.StageSplittingWIM)
	return nil
}

func (s *Service) executePlan(ctx context.Context, plan WorkflowPlan, info image.Info, target device.Device, opts RunOptions, updates chan<- progress.Update, windowsBackend WindowsInstallerBackend, splitter installer.WIMSplitter, out *RunResult) error {
	if plan.Kind == PlanRawWrite {
		return (RawWriteExecutor{service: s}).Execute(ctx, info, target, opts, updates, out)
	}
	executor := WindowsInstallerExecutor{backend: windowsBackend, splitter: splitter, state: s.State}
	if err := executor.Execute(ctx, plan.Windows, info, target, updates, out); err != nil {
		return err
	}
	return s.flushAndVerifyInstaller(ctx, plan.Windows, target, updates, windowsBackend, out)
}

func (s *Service) flushAndVerifyInstaller(ctx context.Context, plan *installer.BuildPlan, target device.Device, updates chan<- progress.Update, backend WindowsInstallerBackend, out *RunResult) error {
	if err := s.State.Transition(Flushing); err != nil {
		return err
	}
	sendStage(ctx, updates, progress.StageFlushing)
	if err := s.Backend.Flush(ctx, target); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	if err := s.State.Transition(VerifyingFilesystem); err != nil {
		return err
	}
	sendStage(ctx, updates, progress.StageVerifyingFilesystem)
	reader, err := backend.OpenInstallerReader(ctx, target)
	if err != nil {
		return err
	}
	verification, verifyErr := verify.VerifyInstaller(ctx, verify.InstallerRequest{
		Reader: reader, TargetSize: target.Size, Manifest: out.installerManifest,
		Options: verify.InstallerOptions{SplitWIMPolicySize: plan.SplitWIMPolicySize(), RequireSplitWIM: plan.InstallStrategy() == installer.SplitWIM},
	})
	if err := errors.Join(verifyErr, reader.Close()); err != nil {
		return err
	}
	out.FilesWritten = verification.FilesVerified
	out.WIMParts = verification.WIMParts
	out.ManifestSHA256 = verification.ManifestSHA256
	out.SemanticVerified = true
	out.SemanticVerification = "raw target GPT, FAT32, paths, sizes, and file hashes verified"
	return nil
}

func inspectImage(ctx context.Context, info image.Info) (image.Info, error) {
	if info.UncompressedSize == 0 {
		return image.InspectContext(ctx, info)
	}
	if info.SHA256 == "" {
		return image.InspectContext(ctx, info)
	}
	if !info.HasRetainedSource() {
		return image.InspectContext(ctx, info)
	}
	return info, nil
}

func (s *Service) unmountDevice(ctx context.Context, target device.Device) error {
	if err := s.State.Transition(Unmounting); err != nil {
		return err
	}
	return s.Backend.Unmount(ctx, target)
}

// newDeviceRelease returns an idempotent cleanup function. Windows keeps
// dismounted-volume handles across writer close, flush, verification, and
// eject, so the service releases them only when the entire workflow finishes.
func newDeviceRelease(backend device.Backend, target device.Device) func() error {
	releaser, ok := backend.(interface{ ReleaseDevice(device.Device) error })
	return func() error {
		if !ok {
			return nil
		}
		ok = false
		if err := releaser.ReleaseDevice(target); err != nil {
			return fmt.Errorf("release device: %w", err)
		}
		return nil
	}
}

func (s *Service) writeImage(ctx context.Context, info image.Info, target device.Device, updates chan<- progress.Update, out *RunResult) error {
	source, err := image.Open(info)
	if err != nil {
		return err
	}
	defer source.Close()
	if err = s.State.Transition(Writing); err != nil {
		return err
	}
	dst, err := s.Backend.OpenWriter(ctx, target)
	if err != nil {
		return err
	}
	writeStage := imageWriteStage(info)
	result, writeErr := writer.Copy(ctx, dst, source, writer.Options{TotalBytes: info.UncompressedSize, TargetSize: target.Size, Progress: updates, WriteStage: writeStage})
	return s.finishWrite(ctx, target, info.SHA256, dst, result, writeErr, updates, out)
}

func imageWriteStage(info image.Info) progress.Stage {
	writeStage := progress.StageWriting
	if info.Compression != image.CompressionNone {
		writeStage = progress.StageDecompressWriting
	}
	return writeStage
}

func (s *Service) finishWrite(ctx context.Context, target device.Device, expectedSHA256 string, dst interface{ Close() error }, result writer.Result, writeErr error, updates chan<- progress.Update, out *RunResult) error {
	// Some backends perform the durability sync while closing the writer. Move
	// the UI to Flushing before Close so that a slow USB device does not appear
	// frozen at 100% writing while its cached data is committed.
	if writeErr == nil {
		if err := s.State.Transition(Flushing); err != nil {
			_ = dst.Close()
			return err
		}
		sendStage(ctx, updates, progress.StageFlushing)
	}
	closeErr := dst.Close()
	if writeErr != nil {
		return writeErr
	}
	out.BytesWritten = result.BytesWritten
	out.SourceSHA256 = result.SHA256
	if closeErr != nil {
		return closeErr
	}
	if result.SHA256 != expectedSHA256 {
		return fmt.Errorf("%w: checksum no longer matches inspected image", writer.ErrSourceChanged)
	}
	if err := s.Backend.Flush(ctx, target); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}

func (s *Service) verifyImage(ctx context.Context, target device.Device, enabled bool, updates chan<- progress.Update, out *RunResult) error {
	if !enabled {
		return nil
	}
	if err := s.State.Transition(Verifying); err != nil {
		return err
	}
	reader, err := s.Backend.OpenReader(ctx, target)
	if err != nil {
		return err
	}
	out.TargetSHA256, err = verify.ReadBack(ctx, reader, out.BytesWritten, out.SourceSHA256, updates)
	closeErr := reader.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	out.Verified = true
	return nil
}

func (s *Service) ejectDevice(ctx context.Context, target device.Device, enabled bool, updates chan<- progress.Update, out *RunResult) error {
	if !enabled {
		return nil
	}
	if err := s.State.Transition(Ejecting); err != nil {
		return err
	}
	sendStage(ctx, updates, progress.StageEjecting)
	if err := s.Backend.Eject(ctx, target); err != nil {
		return fmt.Errorf("eject: %w", err)
	}
	out.Ejected = true
	return nil
}

func (s *Service) finishRun(out *RunResult, runErr error, elapsed time.Duration) {
	out.Elapsed = elapsed
	if out.BytesWritten > 0 && elapsed > 0 {
		out.AverageBytesPerSecond = float64(out.BytesWritten) / elapsed.Seconds()
	}
	if runErr == nil {
		return
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, writer.ErrCancelled) {
		_ = s.State.Transition(Cancelled)
		return
	}
	_ = s.State.Transition(Failed)
}

func sendStage(ctx context.Context, updates chan<- progress.Update, stage progress.Stage) {
	if updates == nil {
		return
	}
	select {
	case updates <- progress.Update{Stage: stage}:
	case <-ctx.Done():
	default:
	}
}
