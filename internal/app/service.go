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

type RunOptions struct{ Verify, Eject bool }
type RunResult struct {
	BytesWritten               uint64
	SourceSHA256, TargetSHA256 string
	Elapsed                    time.Duration
	AverageBytesPerSecond      float64
	Verified, Ejected          bool
}
type Service struct {
	Backend device.Backend
	State   *StateMachine
}

// Run owns the destructive workflow. Safety checks are deliberately repeated
// by the backend immediately before opening the block device.
func (s *Service) Run(ctx context.Context, info image.Info, target device.Device, opts RunOptions, updates chan<- progress.Update) (out RunResult, err error) {
	if s.State == nil {
		s.State = NewStateMachine()
	}
	start := time.Now()
	defer func() {
		out.Elapsed = time.Since(start)
		if out.BytesWritten > 0 && out.Elapsed > 0 {
			out.AverageBytesPerSecond = float64(out.BytesWritten) / out.Elapsed.Seconds()
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, writer.ErrCancelled) {
				_ = s.State.Transition(Cancelled)
			} else {
				_ = s.State.Transition(Failed)
			}
		}
	}()
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
	source, err := image.Open(info)
	if err != nil {
		return out, err
	}
	defer source.Close()
	if err = s.State.Transition(Writing); err != nil {
		return out, err
	}
	dst, err := s.Backend.OpenWriter(ctx, target)
	if err != nil {
		return out, err
	}
	wr, writeErr := writer.Copy(ctx, dst, source, writer.Options{TotalBytes: info.UncompressedSize, TargetSize: target.Size, Progress: updates})
	closeErr := dst.Close()
	if writeErr != nil {
		return out, writeErr
	}
	if closeErr != nil {
		return out, closeErr
	}
	out.BytesWritten = wr.BytesWritten
	out.SourceSHA256 = wr.SHA256
	if err = s.State.Transition(Flushing); err != nil {
		return out, err
	}
	sendStage(ctx, updates, progress.StageFlushing)
	if err = s.Backend.Flush(ctx, target); err != nil {
		return out, fmt.Errorf("flush: %w", err)
	}
	if opts.Verify {
		if err = s.State.Transition(Verifying); err != nil {
			return out, err
		}
		reader, openErr := s.Backend.OpenReader(ctx, target)
		if openErr != nil {
			return out, openErr
		}
		out.TargetSHA256, err = verify.ReadBack(ctx, reader, wr.BytesWritten, wr.SHA256, updates)
		closeErr = reader.Close()
		if err != nil {
			return out, err
		}
		if closeErr != nil {
			return out, closeErr
		}
		out.Verified = true
	}
	if opts.Eject {
		if err = s.State.Transition(Ejecting); err != nil {
			return out, err
		}
		sendStage(ctx, updates, progress.StageEjecting)
		if err = s.Backend.Eject(ctx, target); err != nil {
			return out, fmt.Errorf("eject: %w", err)
		}
		out.Ejected = true
	}
	if err = s.State.Transition(Completed); err != nil {
		return out, err
	}
	return out, nil
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
