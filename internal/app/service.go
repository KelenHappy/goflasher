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

// RunRequest groups the inputs for a destructive device workflow.
type RunRequest struct {
	Image   image.Info
	Target  device.Device
	Options RunOptions
	Updates chan<- progress.Update
}

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
func (s *Service) Run(ctx context.Context, request RunRequest) (RunResult, error) {
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
	out, err := s.runWorkflow(ctx, request)
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

func (s *Service) runWorkflow(ctx context.Context, request RunRequest) (out RunResult, err error) {
	info, plan, err := s.inspectAndPlan(ctx, request.Image, request.Target, request.Updates)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, info.CloseSource()) }()
	out.PlanKind = plan.Kind
	windowsBackend, effectiveSplitter, cleanup, err := s.prepareWindowsInstaller(ctx, info, &plan, request.Updates)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	if err = s.unmountDevice(ctx, request.Target); err != nil {
		return out, err
	}
	releaseDevice := newDeviceRelease(s.Backend, request.Target)
	defer func() { err = errors.Join(err, releaseDevice()) }()
	if err = s.executePlan(ctx, plan, info, request.Target, request.Options, request.Updates, windowsBackend, effectiveSplitter, &out); err != nil {
		return out, err
	}
	if err = releaseDevice(); err != nil {
		return out, err
	}
	operation := workflowOperation{service: s, ctx: ctx, target: request.Target, updates: request.Updates, out: &out}
	if err = operation.ejectDevice(request.Options.Eject); err != nil {
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
	finalized, prepared, cleanup, err := installer.PrepareSplitWIM(ctx, plan.Windows, r, s.InstallerSplitter, func() error {
		return s.startWIMSplit(ctx, updates)
	})
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
		request := rawWriteRequest{
			operation: workflowOperation{ctx: ctx, target: target, updates: updates, out: out},
			info:      info,
			options:   opts,
		}
		return (RawWriteExecutor{service: s}).Execute(request)
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
	verification, verifyErr := verify.VerifyInstaller(ctx, verify.RawTarget{Reader: reader, Size: target.Size}, out.installerManifest, verify.InstallerOptions{
		SplitWIMPolicySize: plan.SplitWIMPolicySize(), RequireSplitWIM: plan.InstallStrategy() == installer.SplitWIM,
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
// dismounted-volume handles across writer close, flush, and verification, so
// the service releases them only after destructive work finishes and before
// any requested eject.
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

type workflowOperation struct {
	service *Service
	ctx     context.Context
	target  device.Device
	updates chan<- progress.Update
	out     *RunResult
}

func (w workflowOperation) writeImage(info image.Info) error {
	source, err := image.Open(info)
	if err != nil {
		return err
	}
	defer source.Close()
	if err = w.service.State.Transition(Writing); err != nil {
		return err
	}
	dst, err := w.service.Backend.OpenWriter(w.ctx, w.target)
	if err != nil {
		return err
	}
	writeStage := imageWriteStage(info)
	result, writeErr := writer.Copy(w.ctx, dst, source, writer.Options{TotalBytes: info.UncompressedSize, TargetSize: w.target.Size, Progress: w.updates, WriteStage: writeStage})
	return w.finishWrite(info.SHA256, dst, result, writeErr)
}

func imageWriteStage(info image.Info) progress.Stage {
	writeStage := progress.StageWriting
	if info.Compression != image.CompressionNone {
		writeStage = progress.StageDecompressWriting
	}
	return writeStage
}

func (w workflowOperation) finishWrite(expectedSHA256 string, dst interface{ Close() error }, result writer.Result, writeErr error) error {
	// Some backends perform the durability sync while closing the writer. Move
	// the UI to Flushing before Close so that a slow USB device does not appear
	// frozen at 100% writing while its cached data is committed.
	if writeErr == nil {
		if err := w.service.State.Transition(Flushing); err != nil {
			_ = dst.Close()
			return err
		}
		sendStage(w.ctx, w.updates, progress.StageFlushing)
	}
	closeErr := dst.Close()
	writeCloseErr := errors.Join(writeErr, closeErr)
	if writeErr != nil {
		return writeCloseErr
	}
	w.out.BytesWritten = result.BytesWritten
	w.out.SourceSHA256 = result.SHA256
	if writeCloseErr != nil {
		return writeCloseErr
	}
	if result.SHA256 != expectedSHA256 {
		return fmt.Errorf("%w: checksum no longer matches inspected image", writer.ErrSourceChanged)
	}
	if err := w.service.Backend.Flush(w.ctx, w.target); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}

func (w workflowOperation) verifyImage(enabled bool) error {
	if !enabled {
		return nil
	}
	if err := w.service.State.Transition(Verifying); err != nil {
		return err
	}
	reader, err := w.service.Backend.OpenReader(w.ctx, w.target)
	if err != nil {
		return err
	}
	w.out.TargetSHA256, err = verify.ReadBack(w.ctx, reader, w.out.BytesWritten, w.out.SourceSHA256, w.updates)
	closeErr := reader.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	w.out.Verified = true
	return nil
}

func (w workflowOperation) ejectDevice(enabled bool) error {
	if !enabled {
		return nil
	}
	if err := w.service.State.Transition(Ejecting); err != nil {
		return err
	}
	sendStage(w.ctx, w.updates, progress.StageEjecting)
	if err := w.service.Backend.Eject(w.ctx, w.target); err != nil {
		return fmt.Errorf("eject: %w", err)
	}
	w.out.Ejected = true
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
