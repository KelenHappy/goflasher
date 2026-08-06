package writer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/goflasher/goflasher/internal/progress"
)

var (
	ErrCancelled      = errors.New("write cancelled")
	ErrTargetTooSmall = errors.New("target too small")
	ErrSourceChanged  = errors.New("source image changed during write")
	ErrWriteFailed    = errors.New("write failed")
)

type Result struct {
	BytesWritten          uint64
	SHA256                string
	Elapsed               time.Duration
	AverageBytesPerSecond float64
}
type Options struct {
	TotalBytes, TargetSize uint64
	BufferSize             int
	Progress               chan<- progress.Update
	Now                    func() time.Time
}

// Copy streams source to target while hashing the exact bytes written.
func Copy(ctx context.Context, dst io.Writer, src io.Reader, opts Options) (Result, error) {
	if opts.TotalBytes > 0 && opts.TargetSize > 0 && opts.TotalBytes > opts.TargetSize {
		return Result{}, ErrTargetTooSmall
	}
	if opts.BufferSize <= 0 {
		opts.BufferSize = 4 << 20
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	start := opts.Now()
	h := sha256.New()
	mw := io.MultiWriter(dst, h)
	buf := make([]byte, opts.BufferSize)
	var written uint64
	for {
		if err := ctx.Err(); err != nil {
			return result(written, h, start, opts.Now()), fmt.Errorf("%w: %v", ErrCancelled, err)
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if opts.TotalBytes > 0 && uint64(n) > opts.TotalBytes-written {
				return result(written, h, start, opts.Now()), fmt.Errorf("%w: expected %d bytes", ErrSourceChanged, opts.TotalBytes)
			}
			wn, err := mw.Write(buf[:n])
			written += uint64(wn)
			if err != nil {
				return result(written, h, start, opts.Now()), fmt.Errorf("%w: %v", ErrWriteFailed, err)
			}
			if wn != n {
				return result(written, h, start, opts.Now()), fmt.Errorf("%w: %v", ErrWriteFailed, io.ErrShortWrite)
			}
			send(ctx, opts.Progress, progress.Calculate(progress.StageWriting, written, opts.TotalBytes, opts.Now().Sub(start)))
		}
		if readErr == io.EOF {
			if opts.TotalBytes > 0 && written != opts.TotalBytes {
				return result(written, h, start, opts.Now()), fmt.Errorf("%w: got %d bytes, expected %d", ErrSourceChanged, written, opts.TotalBytes)
			}
			break
		}
		if readErr != nil {
			return result(written, h, start, opts.Now()), readErr
		}
	}
	return result(written, h, start, opts.Now()), nil
}

func result(n uint64, h interface{ Sum([]byte) []byte }, start, end time.Time) Result {
	elapsed := end.Sub(start)
	r := Result{BytesWritten: n, SHA256: hex.EncodeToString(h.Sum(nil)), Elapsed: elapsed}
	if elapsed > 0 {
		r.AverageBytesPerSecond = float64(n) / elapsed.Seconds()
	}
	return r
}
func send(ctx context.Context, ch chan<- progress.Update, u progress.Update) {
	if ch == nil {
		return
	}
	select {
	case ch <- u:
	case <-ctx.Done():
	default:
	}
}
