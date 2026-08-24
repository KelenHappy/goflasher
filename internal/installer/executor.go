package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/goflasher/goflasher/internal/fat32"
	"github.com/goflasher/goflasher/internal/gpt"
	installeriso "github.com/goflasher/goflasher/internal/installer/iso"
)

const copyBufferSize = 1 << 20

var (
	ErrIncomplete       = errors.New("installer execution incomplete")
	ErrVerification     = errors.New("installer file verification failed")
	ErrSplitterRequired = errors.New("split WIM pipeline is required")
)

// Target is the already-open raw target. The caller may only obtain it after
// NewBuildPlan succeeds; Executor itself never unmounts or opens a backend.
type Target interface {
	io.WriterAt
	Sync() error
}

// SplitPart is one sequential output produced by a WIM-aware split pipeline.
// Data must remain readable until Emit returns.
type SplitPart struct {
	Name string
	Size uint64
	Data io.Reader
}

// WIMSplitter transforms install.wim into valid SWM files. It must emit the
// canonical install.swm, install2.swm, ... sequence and must not emit the
// original WIM or any empty placeholder.
type WIMSplitter interface {
	Split(context.Context, io.Reader, uint64, string, uint64, func(SplitPart) error) error
}

type ExecutionResult struct {
	Complete             bool
	BytesWritten         uint64
	VerificationManifest []VerificationEntry
}

type Executor struct {
	Splitter WIMSplitter
}

// Execute consumes only paths and extents frozen into plan. On every error or
// cancellation Complete remains false, so partial copies or verification can
// never be presented as a successful installer build.
func (x Executor) Execute(ctx context.Context, plan *BuildPlan, source io.ReaderAt, target Target) (result ExecutionResult, err error) {
	run := execution{ctx: ctx, plan: plan, source: source, target: target, splitter: x.Splitter}
	if err := run.execute(); err != nil {
		return run.result, errors.Join(ErrIncomplete, err)
	}
	run.result.Complete = true
	return run.result, nil
}

type execution struct {
	ctx      context.Context
	plan     *BuildPlan
	source   io.ReaderAt
	target   Target
	splitter WIMSplitter
	builder  *fat32.Builder
	result   ExecutionResult
}

func (r *execution) execute() error {
	if err := r.preflight(); err != nil {
		return err
	}
	if err := r.createFilesystem(); err != nil {
		return err
	}
	if err := r.createDirectories(); err != nil {
		return err
	}
	if err := r.copyFiles(); err != nil {
		return err
	}
	if r.plan.strategy == SplitWIM {
		if err := r.copySplitWIM(); err != nil {
			return err
		}
	}
	if err := r.builder.Sync(); err != nil {
		return err
	}
	return r.target.Sync()
}

func (r *execution) preflight() error {
	if !r.hasValidInput() {
		return errors.New("invalid execution input")
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if r.plan.strategy != SplitWIM {
		return nil
	}
	if r.splitter == nil {
		return ErrSplitterRequired
	}
	if preflight, ok := r.splitter.(interface{ Preflight(context.Context) error }); ok {
		return preflight.Preflight(r.ctx)
	}
	return nil
}

func (r *execution) hasValidInput() bool {
	return r.plan != nil && r.source != nil && r.target != nil && len(r.plan.planned) > 0
}

func (r *execution) createFilesystem() error {
	plan := r.plan
	totalLBAs := (plan.esp.StartOffset + plan.esp.Size + 33*logicalSectorSize) / logicalSectorSize
	layout, err := gpt.Build(totalLBAs, logicalSectorSize, nil)
	if err != nil {
		return err
	}
	if layout.PartitionStartLBA*logicalSectorSize != plan.esp.StartOffset || (layout.PartitionEndLBA-layout.PartitionStartLBA+1)*logicalSectorSize != plan.esp.Size {
		return errors.New("GPT layout differs from preflight")
	}
	if err := layout.WriteTo(r.target); err != nil {
		return err
	}
	esp, err := gpt.NewPartitionWriterAt(r.target, layout.PartitionStartLBA, layout.PartitionEndLBA, logicalSectorSize)
	if err != nil {
		return err
	}
	r.builder, err = fat32.NewBuilder(r.ctx, esp, plan.esp.Size, "GOFLASHER")
	return err
}

func (r *execution) createDirectories() error {
	for _, item := range r.plan.planned {
		if item.source.Type == installeriso.Directory {
			if err := r.builder.MkdirAll(item.destination); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *execution) copyFiles() error {
	for _, item := range r.plan.planned {
		if !r.shouldCopy(item) {
			continue
		}
		verified, n, err := copyISOFile(r.ctx, r.builder, r.source, r.plan, item)
		r.recordCopy(verified, n, err)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *execution) shouldCopy(item plannedEntry) bool {
	if item.source.Type != installeriso.File {
		return false
	}
	return r.plan.strategy != SplitWIM || !strings.EqualFold(item.source.Path, "sources/install.wim")
}

func (r *execution) recordCopy(verified VerificationEntry, n uint64, err error) {
	r.result.BytesWritten += n
	if err != nil {
		return
	}
	r.result.VerificationManifest = append(r.result.VerificationManifest, verified)
}

func (r *execution) copySplitWIM() error {
	plan := r.plan
	wim, ok := plannedBySource(plan, "sources/install.wim")
	if !ok {
		return errors.New("planned WIM is missing")
	}
	reader := newExtentReader(r.source, wim.source.Extents, wim.source.Size)
	expectedWIMHash := verificationHash(plan, wim.destination, wim.source.Size)
	if expectedWIMHash == "" {
		return fmt.Errorf("%w: planned WIM hash is missing", ErrVerification)
	}
	nextPart := 1
	err := r.splitter.Split(r.ctx, reader, wim.source.Size, expectedWIMHash, plan.splitSize, func(part SplitPart) error {
		name, err := validateSplitPart(part, nextPart, plan.splitParts, plan.splitSize)
		if err != nil {
			return err
		}
		destination := path.Join(path.Dir(wim.destination), name)
		verified, n, copyErr := copyReaderFile(r.ctx, r.builder, fileCopy{destination: destination, source: part.Data, expectedSize: part.Size})
		r.recordCopy(verified, n, copyErr)
		if copyErr != nil {
			return copyErr
		}
		nextPart++
		return nil
	})
	if err != nil {
		return errors.Join(ErrWIMSplitFailure, err)
	}
	if nextPart != plan.splitParts+1 {
		return fmt.Errorf("split pipeline produced %d of %d planned parts", nextPart-1, plan.splitParts)
	}
	return nil
}

func validateSplitPart(part SplitPart, index, plannedParts int, maxSize uint64) (string, error) {
	if index > plannedParts {
		return "", fmt.Errorf("%w: split pipeline produced too many parts", ErrVerification)
	}
	name := "install.swm"
	if index > 1 {
		name = "install" + strconv.Itoa(index) + ".swm"
	}
	if part.Name != name || part.Data == nil || part.Size == 0 || part.Size > maxSize {
		return "", fmt.Errorf("%w: invalid split part %q", ErrVerification, part.Name)
	}
	return name, nil
}

func plannedBySource(plan *BuildPlan, name string) (plannedEntry, bool) {
	for _, item := range plan.planned {
		if strings.EqualFold(item.source.Path, name) {
			return item, true
		}
	}
	return plannedEntry{}, false
}

func copyISOFile(ctx context.Context, builder *fat32.Builder, source io.ReaderAt, plan *BuildPlan, item plannedEntry) (VerificationEntry, uint64, error) {
	expected := verificationHash(plan, item.destination, item.source.Size)
	if expected == "" {
		return VerificationEntry{}, 0, fmt.Errorf("%w: no preflight hash for %s", ErrVerification, item.destination)
	}
	reader := newExtentReader(source, item.source.Extents, item.source.Size)
	return copyReaderFile(ctx, builder, fileCopy{
		destination:  item.destination,
		source:       reader,
		expectedSize: item.source.Size,
		expectedHash: expected,
	})
}

func verificationHash(plan *BuildPlan, destination string, size uint64) string {
	for _, verification := range plan.verification {
		if strings.EqualFold(strings.ReplaceAll(verification.Path, "\\", "/"), destination) && verification.Size == size {
			return verification.SHA256
		}
	}
	return ""
}

type fileCopy struct {
	destination  string
	source       io.Reader
	expectedSize uint64
	expectedHash string
}

func copyReaderFile(ctx context.Context, builder *fat32.Builder, file fileCopy) (VerificationEntry, uint64, error) {
	if parent := path.Dir(file.destination); parent != "." {
		if err := builder.MkdirAll(parent); err != nil {
			return VerificationEntry{}, 0, err
		}
	}
	dst, err := builder.Create(file.destination)
	if err != nil {
		return VerificationEntry{}, 0, err
	}
	h := sha256.New()
	limited := io.LimitReader(file.source, int64(file.expectedSize)+1)
	written, copyErr := copyContext(ctx, io.MultiWriter(dst, h), limited)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		return VerificationEntry{}, uint64(written), errors.Join(copyErr, closeErr)
	}
	actualHash := hex.EncodeToString(h.Sum(nil))
	if !file.matches(uint64(written), actualHash) {
		return VerificationEntry{}, uint64(written), fmt.Errorf("%w: %s: got %d bytes", ErrVerification, file.destination, written)
	}
	return VerificationEntry{Path: file.destination, Size: uint64(written), SHA256: actualHash}, uint64(written), nil
}

func (f fileCopy) matches(size uint64, hash string) bool {
	if size != f.expectedSize {
		return false
	}
	return f.expectedHash == "" || hash == f.expectedHash
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	return io.CopyBuffer(dst, contextReader{ctx: ctx, reader: src}, make([]byte, copyBufferSize))
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if n == 0 && err == nil {
		return 0, io.ErrNoProgress
	}
	return n, err
}

type extentReader struct {
	source    io.ReaderAt
	extents   []installeriso.Extent
	remaining uint64
	index     int
	offset    uint64
}

type measuredReader struct {
	reader io.Reader
	hash   interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
	count uint64
}

func (r *measuredReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		_, _ = r.hash.Write(p[:n])
		r.count += uint64(n)
	}
	return n, err
}

func newExtentReader(source io.ReaderAt, extents []installeriso.Extent, size uint64) io.Reader {
	return &extentReader{source: source, extents: append([]installeriso.Extent(nil), extents...), remaining: size}
}

func (r *extentReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	for r.index < len(r.extents) && r.offset >= r.extents[r.index].Length {
		r.index++
		r.offset = 0
	}
	if r.index == len(r.extents) {
		return 0, io.ErrUnexpectedEOF
	}
	n := min(uint64(len(p)), r.remaining, r.extents[r.index].Length-r.offset)
	read, err := r.source.ReadAt(p[:n], int64(r.extents[r.index].Offset+r.offset))
	r.offset += uint64(read)
	r.remaining -= uint64(read)
	if err == io.EOF && read > 0 {
		err = nil
	}
	return read, err
}
