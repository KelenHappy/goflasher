package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/image"
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
}

// Service coordinates the device workflow and its state transitions.
type Service struct {
	Backend device.Backend
	State   *StateMachine
}

// Run owns the destructive workflow. Safety checks are deliberately repeated
// by the backend immediately before opening the block device.
func (s *Service) Run(ctx context.Context, info image.Info, target device.Device, opts RunOptions, updates chan<- progress.Update) (RunResult, error) {
	start := time.Now()
	if s.State == nil {
		s.State = NewStateMachine()
	}
	out, err := s.runWorkflow(ctx, info, target, opts, updates)
	s.finishRun(&out, err, time.Since(start))
	return out, err
}

func (s *Service) runWorkflow(ctx context.Context, info image.Info, target device.Device, opts RunOptions, updates chan<- progress.Update) (RunResult, error) {
	var out RunResult
	var err error
	if info.UncompressedSize == 0 || info.SHA256 == "" {
		info, err = image.InspectContext(ctx, info)
		if err != nil {
			return out, err
		}
	}
	if info.UncompressedSize > target.Size {
		return out, writer.ErrTargetTooSmall
	}
	if err = s.State.Transition(Unmounting); err != nil {
		return out, err
	}
	if err = s.Backend.Unmount(ctx, target); err != nil {
		return out, err
	}
	// Windows keeps dismounted-volume handles across writer close, flush,
	// verification, and eject. Release them only when the complete operation
	// leaves this scope.
	if releaser, ok := s.Backend.(interface{ ReleaseDevice(device.Device) error }); ok {
		defer releaser.ReleaseDevice(target)
	}
	if err = s.writeImage(ctx, info, target, updates, &out); err != nil {
		return out, err
	}
	if err = s.verifyImage(ctx, target, opts.Verify, updates, &out); err != nil {
		return out, err
	}
	if err = s.ejectDevice(ctx, target, opts.Eject, updates, &out); err != nil {
		return out, err
	}
	if err = s.State.Transition(Completed); err != nil {
		return out, err
	}
	return out, nil
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
	writeStage := progress.StageWriting
	if info.Compression != image.CompressionNone {
		writeStage = progress.StageDecompressWriting
	}
	wr, writeErr := writer.Copy(ctx, dst, source, writer.Options{TotalBytes: info.UncompressedSize, TargetSize: target.Size, Progress: updates, WriteStage: writeStage})
	// Some backends perform the durability sync while closing the writer. Move
	// the UI to Flushing before Close so that a slow USB device does not appear
	// frozen at 100% writing while its cached data is committed.
	if writeErr == nil {
		if err = s.State.Transition(Flushing); err != nil {
			_ = dst.Close()
			return err
		}
		sendStage(ctx, updates, progress.StageFlushing)
	}
	closeErr := dst.Close()
	if writeErr != nil {
		return writeErr
	}
	out.BytesWritten = wr.BytesWritten
	out.SourceSHA256 = wr.SHA256
	if closeErr != nil {
		return closeErr
	}
	if wr.SHA256 != info.SHA256 {
		return fmt.Errorf("%w: checksum no longer matches inspected image", writer.ErrSourceChanged)
	}
	if err = s.Backend.Flush(ctx, target); err != nil {
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
