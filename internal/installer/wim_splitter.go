package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/goflasher/goflasher/internal/wim"
)

type wimSplitFunc func(context.Context, string, string, uint64, wim.ProgressFunc) ([]wim.Part, error)

// NativeWIMSplitter stages only install.wim in a private temporary directory,
// invokes the platform WIM backend, validates its complete output set, and
// streams each part to the executor. Backend details never escape internal/wim.
type NativeWIMSplitter struct {
	split wimSplitFunc
	probe func() error
}

type wimPreparer interface {
	PrepareWithProgress(context.Context, io.Reader, uint64, string, uint64, func() error) (WIMSplitter, io.Closer, error)
}

type preparedNativeWIM struct {
	temporary       string
	parts           []wim.Part
	sourceSize      uint64
	expectedHash    string
	partSize        uint64
	mu              sync.Mutex
	closed, emitted bool
}

func NewNativeWIMSplitter() *NativeWIMSplitter {
	return &NativeWIMSplitter{split: wim.Split, probe: wim.Probe}
}

func (s *NativeWIMSplitter) Preflight(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.probe == nil {
		return ErrSplitterRequired
	}
	return s.probe()
}

// PrepareSplitWIM stages, parses, splits, and validates install.wim without a
// target handle. The returned splitter only replays the retained validated
// parts and cleanup removes all staged data.
func PrepareSplitWIM(ctx context.Context, plan *BuildPlan, source io.ReaderAt, splitter WIMSplitter, onSplitting func() error) (*BuildPlan, WIMSplitter, io.Closer, error) {
	if plan == nil || source == nil || plan.strategy != SplitWIM {
		return nil, nil, nil, fmt.Errorf("%w: invalid split preparation input", ErrVerification)
	}
	preparer, ok := splitter.(wimPreparer)
	if !ok {
		return nil, nil, nil, ErrSplitterRequired
	}
	wimEntry, ok := plannedBySource(plan, "sources/install.wim")
	if !ok {
		return nil, nil, nil, fmt.Errorf("%w: planned WIM is missing", ErrVerification)
	}
	expected := verificationHash(plan, wimEntry.destination, wimEntry.source.Size)
	if expected == "" {
		return nil, nil, nil, fmt.Errorf("%w: planned WIM hash is missing", ErrVerification)
	}
	reader := newExtentReader(source, wimEntry.source.Extents, wimEntry.source.Size)
	prepared, cleanup, err := preparer.PrepareWithProgress(ctx, reader, wimEntry.source.Size, expected, plan.splitSize, onSplitting)
	if err != nil {
		return nil, nil, nil, err
	}
	if prepared == nil || cleanup == nil {
		if cleanup != nil {
			_ = cleanup.Close()
		}
		return nil, nil, nil, fmt.Errorf("%w: split preparation returned incomplete result", ErrVerification)
	}
	geometry, ok := prepared.(interface{ PreparedPartSizes() []uint64 })
	if !ok {
		_ = cleanup.Close()
		return nil, nil, nil, fmt.Errorf("%w: prepared split geometry is unavailable", ErrVerification)
	}
	finalized, err := plan.withPreparedSplitGeometry(geometry.PreparedPartSizes())
	if err != nil {
		_ = cleanup.Close()
		return nil, nil, nil, err
	}
	return finalized, prepared, cleanup, nil
}

func (s *NativeWIMSplitter) Split(ctx context.Context, source io.Reader, sourceSize uint64, expectedSHA256 string, partSize uint64, emit func(SplitPart) error) (err error) {
	prepared, cleanup, err := s.Prepare(ctx, source, sourceSize, expectedSHA256, partSize)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, cleanup.Close()) }()
	return prepared.Split(ctx, nil, sourceSize, expectedSHA256, partSize, emit)
}

// Prepare performs every source-dependent WIM operation and retains the
// validated parts until Close. Callers run it before opening the target.
func (s *NativeWIMSplitter) Prepare(ctx context.Context, source io.Reader, sourceSize uint64, expectedSHA256 string, partSize uint64) (_ WIMSplitter, cleanup io.Closer, err error) {
	return s.PrepareWithProgress(ctx, source, sourceSize, expectedSHA256, partSize, nil)
}

func (s *NativeWIMSplitter) PrepareWithProgress(ctx context.Context, source io.Reader, sourceSize uint64, expectedSHA256 string, partSize uint64, onSplitting func() error) (_ WIMSplitter, cleanup io.Closer, err error) {
	if s == nil || s.split == nil || source == nil || sourceSize == 0 || expectedSHA256 == "" || partSize == 0 || partSize >= maxFATFileSize {
		return nil, nil, fmt.Errorf("%w: invalid native split input", ErrVerification)
	}
	temporary, err := os.MkdirTemp("", "goflasher-wim-*")
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(temporary, 0700); err != nil {
		_ = os.RemoveAll(temporary)
		return nil, nil, err
	}
	success := false
	defer func() {
		if !success {
			err = errors.Join(err, os.RemoveAll(temporary))
		}
	}()
	sourcePath := filepath.Join(temporary, "install.wim")
	if err := stageWIM(ctx, sourcePath, source, sourceSize, expectedSHA256); err != nil {
		return nil, nil, err
	}
	if onSplitting != nil {
		if err := onSplitting(); err != nil {
			return nil, nil, err
		}
	}
	output := filepath.Join(temporary, "split")
	if err := os.Mkdir(output, 0700); err != nil {
		return nil, nil, err
	}
	parts, err := s.split(ctx, sourcePath, output, partSize, nil)
	if err != nil { // A native call may return only after cancellation; never emit afterwards.
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := validateSplitParts(parts, output, sourceSize, partSize); err != nil {
		return nil, nil, err
	}
	prepared := &preparedNativeWIM{temporary: temporary, parts: append([]wim.Part(nil), parts...), sourceSize: sourceSize, expectedHash: expectedSHA256, partSize: partSize}
	success = true
	return prepared, prepared, nil
}

func (p *preparedNativeWIM) Split(ctx context.Context, _ io.Reader, sourceSize uint64, expectedSHA256 string, partSize uint64, emit func(SplitPart) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.emitted || emit == nil || sourceSize != p.sourceSize || expectedSHA256 != p.expectedHash || partSize != p.partSize {
		return fmt.Errorf("%w: invalid prepared split use", ErrVerification)
	}
	p.emitted = true
	for index, part := range p.parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, err := os.Open(part.Path)
		if err != nil {
			return err
		}
		name := "install.swm"
		if index > 0 {
			name = fmt.Sprintf("install%d.swm", index+1)
		}
		emitErr := emit(SplitPart{Name: name, Size: part.Size, Data: file})
		closeErr := file.Close()
		if emitErr != nil || closeErr != nil {
			return errors.Join(emitErr, closeErr)
		}
	}
	return nil
}

func (p *preparedNativeWIM) PreparedPartSizes() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	sizes := make([]uint64, len(p.parts))
	for i, part := range p.parts {
		sizes[i] = part.Size
	}
	return sizes
}

func (p *preparedNativeWIM) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	return os.RemoveAll(p.temporary)
}

func stageWIM(ctx context.Context, path string, source io.Reader, size uint64, expectedHash string) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	hash := sha256.New()
	written, err := copyContext(ctx, io.MultiWriter(file, hash), io.LimitReader(source, int64(size)+1))
	if err != nil {
		return err
	}
	if uint64(written) != size || hex.EncodeToString(hash.Sum(nil)) != expectedHash {
		return fmt.Errorf("%w: staged install.wim differs from preflight", ErrVerification)
	}
	return file.Sync()
}

func validateSplitParts(parts []wim.Part, output string, sourceSize, policy uint64) error {
	if len(parts) == 0 {
		return fmt.Errorf("%w: split produced no parts", ErrVerification)
	}
	canonicalOutput, err := filepath.EvalSymlinks(output)
	if err != nil {
		return fmt.Errorf("%w: invalid split output directory", ErrVerification)
	}
	var total uint64
	for index, part := range parts {
		want := "install.swm"
		if index > 0 {
			want = fmt.Sprintf("install%d.swm", index+1)
		}
		canonical, err := filepath.EvalSymlinks(part.Path)
		if err != nil || filepath.Dir(canonical) != canonicalOutput || filepath.Base(canonical) != want || part.Size == 0 || part.Size > policy || part.Size >= maxFATFileSize {
			return fmt.Errorf("%w: invalid split part %q", ErrVerification, part.Path)
		}
		info, err := os.Stat(canonical)
		if err != nil || info.IsDir() || uint64(info.Size()) != part.Size {
			return fmt.Errorf("%w: split part size differs for %q", ErrVerification, part.Path)
		}
		if total > math.MaxUint64-part.Size {
			return fmt.Errorf("%w: split size overflow", ErrVerification)
		}
		total += part.Size
	}
	// Split WIMs repeat small metadata. Permit substantial encoding variance,
	// while rejecting obviously truncated or explosively large output sets.
	minimum := sourceSize - sourceSize/4
	maximum := sourceSize + sourceSize/4
	if uint64(len(parts)) > (math.MaxUint64-maximum)/(1<<20) {
		return fmt.Errorf("%w: split overhead overflow", ErrVerification)
	}
	maximum += uint64(len(parts)) * (1 << 20)
	if total < minimum || total > maximum {
		return fmt.Errorf("%w: unreasonable split size %d for source %d", ErrVerification, total, sourceSize)
	}
	return nil
}
