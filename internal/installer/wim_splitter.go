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

type wimSplitFunc func(context.Context, wim.Request) ([]wim.Part, error)

// SplitRequest describes one install.wim split job: where the bytes come
// from, how many there are, the hash they must match, and the part policy.
type SplitRequest struct {
	Source         io.Reader
	SourceSize     uint64
	ExpectedSHA256 string
	PartSize       uint64
}

func (r SplitRequest) valid() bool {
	return r.Source != nil && r.SourceSize != 0 && r.ExpectedSHA256 != "" && r.PartSize != 0 && r.PartSize < maxFATFileSize
}

// matches reports whether both requests describe the same job, ignoring the
// reader. Prepared splitters replay retained parts and never read Source.
func (r SplitRequest) matches(other SplitRequest) bool {
	return r.SourceSize == other.SourceSize && r.ExpectedSHA256 == other.ExpectedSHA256 && r.PartSize == other.PartSize
}

// SplitPreparation is the input to PrepareSplitWIM.
type SplitPreparation struct {
	Plan     *BuildPlan
	Source   io.ReaderAt
	Splitter WIMSplitter
	// OnSplitting runs after staging succeeds and before the backend splits.
	OnSplitting func() error
}

func (in SplitPreparation) valid() bool {
	return in.Plan != nil && in.Source != nil && in.Plan.strategy == SplitWIM
}

// PreparedSplitWIM is the result of PrepareSplitWIM. Splitter only replays
// the retained validated parts and Cleanup removes all staged data.
type PreparedSplitWIM struct {
	Plan     *BuildPlan
	Splitter WIMSplitter
	Cleanup  io.Closer
}

// NativeWIMSplitter stages only install.wim in a private temporary directory,
// invokes the platform WIM backend, validates its complete output set, and
// streams each part to the executor. Backend details never escape internal/wim.
type NativeWIMSplitter struct {
	split wimSplitFunc
	probe func() error
}

type wimPreparer interface {
	PrepareWithProgress(context.Context, SplitRequest, func() error) (WIMSplitter, io.Closer, error)
}

type preparedNativeWIM struct {
	temporary       string
	parts           []wim.Part
	request         SplitRequest
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
// target handle.
func PrepareSplitWIM(ctx context.Context, in SplitPreparation) (*PreparedSplitWIM, error) {
	if !in.valid() {
		return nil, fmt.Errorf("%w: invalid split preparation input", ErrVerification)
	}
	preparer, ok := in.Splitter.(wimPreparer)
	if !ok {
		return nil, ErrSplitterRequired
	}
	request, err := plannedSplitRequest(in.Plan, in.Source)
	if err != nil {
		return nil, err
	}
	prepared, cleanup, err := prepareComplete(ctx, preparer, request, in.OnSplitting)
	if err != nil {
		return nil, err
	}
	finalized, err := finalizeSplitGeometry(in.Plan, prepared)
	if err != nil {
		return nil, errors.Join(err, cleanup.Close())
	}
	return &PreparedSplitWIM{Plan: finalized, Splitter: prepared, Cleanup: cleanup}, nil
}

// prepareComplete runs the preparer and rejects a partial result so callers
// never receive a splitter without its cleanup or vice versa.
func prepareComplete(ctx context.Context, preparer wimPreparer, request SplitRequest, onSplitting func() error) (WIMSplitter, io.Closer, error) {
	prepared, cleanup, err := preparer.PrepareWithProgress(ctx, request, onSplitting)
	if err != nil {
		return nil, nil, err
	}
	if prepared != nil && cleanup != nil {
		return prepared, cleanup, nil
	}
	if cleanup != nil {
		_ = cleanup.Close()
	}
	return nil, nil, fmt.Errorf("%w: split preparation returned incomplete result", ErrVerification)
}

// plannedSplitRequest resolves the planned install.wim entry and its
// verification hash into a split request reading straight from the ISO.
func plannedSplitRequest(plan *BuildPlan, source io.ReaderAt) (SplitRequest, error) {
	entry, ok := plannedBySource(plan, "sources/install.wim")
	if !ok {
		return SplitRequest{}, fmt.Errorf("%w: planned WIM is missing", ErrVerification)
	}
	expected := verificationHash(plan, entry.destination, entry.source.Size)
	if expected == "" {
		return SplitRequest{}, fmt.Errorf("%w: planned WIM hash is missing", ErrVerification)
	}
	return SplitRequest{
		Source:         newExtentReader(source, entry.source.Extents, entry.source.Size),
		SourceSize:     entry.source.Size,
		ExpectedSHA256: expected,
		PartSize:       plan.splitSize,
	}, nil
}

func finalizeSplitGeometry(plan *BuildPlan, prepared WIMSplitter) (*BuildPlan, error) {
	geometry, ok := prepared.(interface{ PreparedPartSizes() []uint64 })
	if !ok {
		return nil, fmt.Errorf("%w: prepared split geometry is unavailable", ErrVerification)
	}
	return plan.withPreparedSplitGeometry(geometry.PreparedPartSizes())
}

func (s *NativeWIMSplitter) Split(ctx context.Context, request SplitRequest, emit func(SplitPart) error) (err error) {
	prepared, cleanup, err := s.Prepare(ctx, request)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, cleanup.Close()) }()
	return prepared.Split(ctx, request, emit)
}

// Prepare performs every source-dependent WIM operation and retains the
// validated parts until Close. Callers run it before opening the target.
func (s *NativeWIMSplitter) Prepare(ctx context.Context, request SplitRequest) (WIMSplitter, io.Closer, error) {
	return s.PrepareWithProgress(ctx, request, nil)
}

func (s *NativeWIMSplitter) PrepareWithProgress(ctx context.Context, request SplitRequest, onSplitting func() error) (_ WIMSplitter, cleanup io.Closer, err error) {
	if !s.accepts(request) {
		return nil, nil, fmt.Errorf("%w: invalid native split input", ErrVerification)
	}
	temporary, err := newSplitWorkspace()
	if err != nil {
		return nil, nil, err
	}
	parts, err := s.stageAndSplit(ctx, temporary, request, onSplitting)
	if err != nil {
		return nil, nil, errors.Join(err, os.RemoveAll(temporary))
	}
	prepared := &preparedNativeWIM{temporary: temporary, parts: parts, request: request}
	return prepared, prepared, nil
}

// accepts reports whether the splitter has a backend and the request is
// complete enough to stage and split.
func (s *NativeWIMSplitter) accepts(request SplitRequest) bool {
	return s != nil && s.split != nil && request.valid()
}

// newSplitWorkspace creates a private temporary directory for staging.
func newSplitWorkspace() (string, error) {
	temporary, err := os.MkdirTemp("", "goflasher-wim-*")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(temporary, 0700); err != nil {
		return "", errors.Join(err, os.RemoveAll(temporary))
	}
	return temporary, nil
}

// stageAndSplit copies the verified source into the workspace, runs the
// backend, and returns a private copy of the validated part list.
func (s *NativeWIMSplitter) stageAndSplit(ctx context.Context, temporary string, request SplitRequest, onSplitting func() error) ([]wim.Part, error) {
	sourcePath := filepath.Join(temporary, "install.wim")
	if err := stageWIM(ctx, sourcePath, request); err != nil {
		return nil, err
	}
	if onSplitting != nil {
		if err := onSplitting(); err != nil {
			return nil, err
		}
	}
	output := filepath.Join(temporary, "split")
	if err := os.Mkdir(output, 0700); err != nil {
		return nil, err
	}
	parts, err := s.split(ctx, wim.Request{SourcePath: sourcePath, OutputDir: output, PartSize: request.PartSize})
	if err != nil { // A native call may return only after cancellation; never emit afterwards.
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSplitParts(parts, output, request.SourceSize, request.PartSize); err != nil {
		return nil, err
	}
	return append([]wim.Part(nil), parts...), nil
}

func (p *preparedNativeWIM) Split(ctx context.Context, request SplitRequest, emit func(SplitPart) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.replayable(request, emit) {
		return fmt.Errorf("%w: invalid prepared split use", ErrVerification)
	}
	p.emitted = true
	for index, part := range p.parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emitRetainedPart(index, part, emit); err != nil {
			return err
		}
	}
	return nil
}

// replayable reports whether the retained parts may be emitted exactly once
// for the job they were prepared for. Callers hold p.mu.
func (p *preparedNativeWIM) replayable(request SplitRequest, emit func(SplitPart) error) bool {
	return !p.closed && !p.emitted && emit != nil && p.request.matches(request)
}

func emitRetainedPart(index int, part wim.Part, emit func(SplitPart) error) error {
	file, err := os.Open(part.Path)
	if err != nil {
		return err
	}
	emitErr := emit(SplitPart{Name: swmPartName(index), Size: part.Size, Data: file})
	return errors.Join(emitErr, file.Close())
}

// swmPartName returns the Windows Setup name for the zero-based part index.
func swmPartName(index int) string {
	if index == 0 {
		return "install.swm"
	}
	return fmt.Sprintf("install%d.swm", index+1)
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

func stageWIM(ctx context.Context, path string, request SplitRequest) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	hash := sha256.New()
	written, err := copyContext(ctx, io.MultiWriter(file, hash), io.LimitReader(request.Source, int64(request.SourceSize)+1))
	if err != nil {
		return err
	}
	if uint64(written) != request.SourceSize || hex.EncodeToString(hash.Sum(nil)) != request.ExpectedSHA256 {
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
		if err := validateSplitPartFile(part, index, canonicalOutput, policy); err != nil {
			return err
		}
		if total > math.MaxUint64-part.Size {
			return fmt.Errorf("%w: split size overflow", ErrVerification)
		}
		total += part.Size
	}
	return validateSplitTotal(total, len(parts), sourceSize)
}

// validateSplitPartFile checks that one reported part is a regular file of
// the expected name and size inside the split output directory.
func validateSplitPartFile(part wim.Part, index int, canonicalOutput string, policy uint64) error {
	canonical, err := filepath.EvalSymlinks(part.Path)
	if err != nil || !splitPartAccepted(part, canonical, canonicalOutput, index, policy) {
		return fmt.Errorf("%w: invalid split part %q", ErrVerification, part.Path)
	}
	info, err := os.Stat(canonical)
	if err != nil || !splitPartFileMatches(info, part.Size) {
		return fmt.Errorf("%w: split part size differs for %q", ErrVerification, part.Path)
	}
	return nil
}

func splitPartAccepted(part wim.Part, canonical, canonicalOutput string, index int, policy uint64) bool {
	return splitPartLocated(canonical, canonicalOutput, index) && splitPartSized(part.Size, policy)
}

func splitPartFileMatches(info os.FileInfo, size uint64) bool {
	return !info.IsDir() && uint64(info.Size()) == size
}

func splitPartLocated(canonical, canonicalOutput string, index int) bool {
	return filepath.Dir(canonical) == canonicalOutput && filepath.Base(canonical) == swmPartName(index)
}

func splitPartSized(size, policy uint64) bool {
	return size != 0 && size <= policy && size < maxFATFileSize
}

// validateSplitTotal rejects obviously truncated or explosively large output
// sets. Split WIMs repeat small metadata, so substantial variance is allowed.
func validateSplitTotal(total uint64, partCount int, sourceSize uint64) error {
	minimum := sourceSize - sourceSize/4
	maximum := sourceSize + sourceSize/4
	if uint64(partCount) > (math.MaxUint64-maximum)/(1<<20) {
		return fmt.Errorf("%w: split overhead overflow", ErrVerification)
	}
	maximum += uint64(partCount) * (1 << 20)
	if total < minimum || total > maximum {
		return fmt.Errorf("%w: unreasonable split size %d for source %d", ErrVerification, total, sourceSize)
	}
	return nil
}
