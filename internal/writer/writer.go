package writer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"time"

	"github.com/goflasher/goflasher/internal/progress"
)

const defaultBufferSize = 4 << 20

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
	WriteStage             progress.Stage // defaults to StageWriting when zero
}

// tooLargeForTarget reports whether a known image size exceeds a known target
// size; a zero on either side means the size is unknown and cannot be checked.
func (o Options) tooLargeForTarget() bool {
	if o.TotalBytes == 0 || o.TargetSize == 0 {
		return false
	}
	return o.TotalBytes > o.TargetSize
}

// withDefaults fills the optional fields the copy loop relies on.
func (o Options) withDefaults() Options {
	if o.BufferSize <= 0 {
		o.BufferSize = defaultBufferSize
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.WriteStage == "" {
		o.WriteStage = progress.StageWriting
	}
	return o
}

// Copy streams source to target while hashing the exact bytes written.
func Copy(ctx context.Context, dst io.Writer, src io.Reader, opts Options) (Result, error) {
	if opts.tooLargeForTarget() {
		return Result{}, ErrTargetTooSmall
	}
	return newCopier(dst, opts.withDefaults()).run(ctx, src)
}

// copier carries the per-run state of one Copy so the loop stays flat.
type copier struct {
	opts    Options
	sink    io.Writer // target and hash together
	hash    hash.Hash
	start   time.Time
	written uint64
}

func newCopier(dst io.Writer, opts Options) *copier {
	h := sha256.New()
	return &copier{opts: opts, sink: io.MultiWriter(dst, h), hash: h, start: opts.Now()}
}

func (c *copier) run(ctx context.Context, src io.Reader) (Result, error) {
	buf := make([]byte, c.opts.BufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return c.result(), fmt.Errorf("%w: %v", ErrCancelled, err)
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if err := c.consume(ctx, buf[:n]); err != nil {
				return c.result(), err
			}
		}
		done, err := c.afterRead(readErr)
		if err != nil {
			return c.result(), err
		}
		if done {
			return c.result(), nil
		}
	}
}

// consume writes one chunk to the target and the hash, then reports progress.
func (c *copier) consume(ctx context.Context, chunk []byte) error {
	if c.opts.TotalBytes > 0 && uint64(len(chunk)) > c.opts.TotalBytes-c.written {
		return fmt.Errorf("%w: expected %d bytes", ErrSourceChanged, c.opts.TotalBytes)
	}
	wn, err := c.sink.Write(chunk)
	c.written += uint64(wn)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWriteFailed, err)
	}
	if wn != len(chunk) {
		return fmt.Errorf("%w: %v", ErrWriteFailed, io.ErrShortWrite)
	}
	send(ctx, c.opts.Progress, progress.Calculate(c.opts.WriteStage, c.written, c.opts.TotalBytes, c.elapsed()))
	return nil
}

// afterRead turns the read outcome into "the copy is complete" or a failure.
func (c *copier) afterRead(readErr error) (bool, error) {
	switch {
	case readErr == nil:
		return false, nil
	case readErr != io.EOF:
		return false, readErr
	case c.opts.TotalBytes > 0 && c.written != c.opts.TotalBytes:
		return false, fmt.Errorf("%w: got %d bytes, expected %d", ErrSourceChanged, c.written, c.opts.TotalBytes)
	default:
		return true, nil
	}
}

func (c *copier) elapsed() time.Duration { return c.opts.Now().Sub(c.start) }

func (c *copier) result() Result {
	elapsed := c.elapsed()
	r := Result{BytesWritten: c.written, SHA256: hex.EncodeToString(c.hash.Sum(nil)), Elapsed: elapsed}
	if elapsed > 0 {
		r.AverageBytesPerSecond = float64(c.written) / elapsed.Seconds()
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
