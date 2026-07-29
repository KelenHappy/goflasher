package verify

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

var ErrMismatch = errors.New("verification failed")

// ReadBack hashes exactly size bytes from the target and compares them with
// the digest produced while writing. It never reads unused device capacity.
func ReadBack(ctx context.Context, target io.Reader, size uint64, expected string, updates chan<- progress.Update) (string, error) {
	h := sha256.New()
	limited := io.LimitReader(target, int64(size))
	buf := make([]byte, 4<<20)
	start := time.Now()
	var read uint64
	for read < size {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := limited.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
			read += uint64(n)
			send(ctx, updates, progress.Calculate(progress.StageVerifying, read, size, time.Since(start)))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if read != size {
		return "", fmt.Errorf("%w: target truncated at %d of %d bytes", ErrMismatch, read, size)
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return actual, fmt.Errorf("%w: expected %s, got %s", ErrMismatch, expected, actual)
	}
	return actual, nil
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
