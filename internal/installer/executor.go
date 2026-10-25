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
	Split(context.Context, SplitRequest, func(SplitPart) error) error
}

type ExecutionResult struct {
	Complete             bool
	BytesWritten         uint64
	VerificationManifest []VerificationEntry
}

type Executor struct {
	Splitter WIMSplitter
	// Progress, when set, receives the file bytes copied so far and the total
	// planned file bytes. A split install.wim is counted at its source size, so
	// the reported value is capped at the total.
	Progress func(copied, total uint64)
}

// Execute consumes only paths and extents frozen into plan. On every error or
// cancellation Complete remains false, so partial copies or verification can
// never be presented as a successful installer build.
func (x Executor) Execute(ctx context.Context, plan *BuildPlan, source io.ReaderAt, target Target) (result ExecutionResult, err error) {
	run := execution{ctx: ctx, plan: plan, source: source, target: target, splitter: x.Splitter, progress: x.Progress}
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
	progress func(copied, total uint64)
	// copied and total are the progress counters reported through progress.
	copied, total uint64
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
	r.total = r.plannedFileBytes()
	r.reportCopied(0) // start the stage's throughput clock before the first copy
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
	builder, err := fat32.NewBuilder(r.ctx, esp, plan.esp.Size, "GOFLASHER")
	r.builder = builder
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
		copied, err := r.copyISOFile(item)
		r.recordCopy(copied, err)
		if err != nil {
			return err
		}
	}
	return nil
}

// plannedFileBytes is the size of every planned file, install.wim included
// whether it is copied whole or as split parts.
func (r *execution) plannedFileBytes() uint64 {
	var total uint64
	for _, item := range r.plan.planned {
		if item.source.Type == installeriso.File {
			total += item.source.Size
		}
	}
	return total
}

// reportCopied counts n more copied bytes and forwards the running total.
func (r *execution) reportCopied(n int) {
	r.copied += uint64(n)
	if r.progress != nil {
		r.progress(min(r.copied, r.total), r.total)
	}
}

func (r *execution) shouldCopy(item plannedEntry) bool {
	if item.source.Type != installeriso.File {
		return false
	}
	return r.plan.strategy != SplitWIM || !item.isInstallWIM()
}

func (r *execution) recordCopy(copied copyResult, err error) {
	r.result.BytesWritten += copied.written
	if err != nil {
		return
	}
	r.result.VerificationManifest = append(r.result.VerificationManifest, copied.entry)
}

func (r *execution) copySplitWIM() error {
	plan := r.plan
	wim, ok := plan.installWIM()
	if !ok {
		return errors.New("planned WIM is missing")
	}
	reader := newExtentReader(r.source, wim.source)
	expectedWIMHash := plan.verificationHash(wim)
	if expectedWIMHash == "" {
		return fmt.Errorf("%w: planned WIM hash is missing", ErrVerification)
	}
	parts := newSplitSequence(plan)
	request := SplitRequest{Source: reader, SourceSize: wim.source.Size, ExpectedSHA256: expectedWIMHash, PartSize: plan.splitSize}
	err := r.splitter.Split(r.ctx, request, func(part SplitPart) error {
		name, err := parts.accept(part)
		if err != nil {
			return err
		}
		destination := path.Join(path.Dir(wim.destination), name)
		copied, copyErr := copyReaderFile(r.ctx, r.builder, fileCopy{destination: destination, source: part.Data, expectedSize: part.Size, onCopy: r.reportCopied})
		r.recordCopy(copied, copyErr)
		if copyErr != nil {
			return copyErr
		}
		parts.advance()
		return nil
	})
	if err != nil {
		return errors.Join(ErrWIMSplitFailure, err)
	}
	return parts.complete()
}

// splitPartNumber counts SWM parts from 1.
type splitPartNumber int

// name is the canonical SWM sequence: install.swm, install2.swm, ...
func (n splitPartNumber) name() string {
	if n > 1 {
		return "install" + strconv.Itoa(int(n)) + ".swm"
	}
	return "install.swm"
}

// splitSequence checks that a split pipeline emits exactly the planned parts,
// in canonical order and within the planned part size.
type splitSequence struct {
	next, planned splitPartNumber
	maxSize       uint64
}

func newSplitSequence(plan *BuildPlan) splitSequence {
	return splitSequence{next: 1, planned: splitPartNumber(plan.splitParts), maxSize: plan.splitSize}
}

// accept validates part as the next expected part and returns its canonical
// name. advance moves past it once it has been copied.
func (s *splitSequence) accept(part SplitPart) (string, error) {
	if s.next > s.planned {
		return "", fmt.Errorf("%w: split pipeline produced too many parts", ErrVerification)
	}
	if err := s.check(part); err != nil {
		return "", err
	}
	return s.next.name(), nil
}

// check reports why a part is unusable instead of collapsing every rejection
// into one message.
func (s *splitSequence) check(p SplitPart) error {
	name := s.next.name()
	switch {
	case p.Name != name:
		return fmt.Errorf("%w: split part %q, want %q", ErrVerification, p.Name, name)
	case p.Data == nil:
		return fmt.Errorf("%w: split part %q carries no data", ErrVerification, p.Name)
	case p.Size == 0:
		return fmt.Errorf("%w: split part %q is empty", ErrVerification, p.Name)
	case p.Size > s.maxSize:
		return fmt.Errorf("%w: split part %q is %d bytes, over the %d byte part size", ErrVerification, p.Name, p.Size, s.maxSize)
	}
	return nil
}

func (s *splitSequence) advance() { s.next++ }

// complete reports a pipeline that stopped before every planned part.
func (s *splitSequence) complete() error {
	if s.next != s.planned+1 {
		return fmt.Errorf("split pipeline produced %d of %d planned parts", s.next-1, s.planned)
	}
	return nil
}

// installWIM returns the planned sources/install.wim entry.
func (p *BuildPlan) installWIM() (plannedEntry, bool) {
	for _, item := range p.planned {
		if item.isInstallWIM() {
			return item, true
		}
	}
	return plannedEntry{}, false
}

func (e plannedEntry) isInstallWIM() bool {
	return strings.EqualFold(e.source.Path, string(keyInstallWIM))
}

// copyISOFile copies one planned entry straight out of the ISO extents. The
// plan, source and builder it needs are already the execution's own.
func (r *execution) copyISOFile(item plannedEntry) (copyResult, error) {
	expected := r.plan.verificationHash(item)
	if expected == "" {
		return copyResult{}, fmt.Errorf("%w: no preflight hash for %s", ErrVerification, item.destination)
	}
	reader := newExtentReader(r.source, item.source)
	return copyReaderFile(r.ctx, r.builder, fileCopy{
		destination:  item.destination,
		source:       reader,
		expectedSize: item.source.Size,
		expectedHash: expected,
		onCopy:       r.reportCopied,
	})
}

// verificationHash is the preflight SHA-256 recorded for item, or "" when the
// preflight recorded none at item's destination and size.
func (p *BuildPlan) verificationHash(item plannedEntry) string {
	for _, verification := range p.verification {
		if strings.EqualFold(strings.ReplaceAll(verification.Path, "\\", "/"), item.destination) && verification.Size == item.source.Size {
			return verification.SHA256
		}
	}
	return ""
}

// copyResult is one copied file: the bytes that reached the filesystem, and
// its verification entry once the copy completed and matched.
type copyResult struct {
	entry   VerificationEntry
	written uint64
}

type fileCopy struct {
	destination  string
	source       io.Reader
	expectedSize uint64
	expectedHash string
	// onCopy, when set, is told how many bytes each successful write stored.
	onCopy func(int)
}

// copyCounter reports every chunk that reached the filesystem. It follows the
// file in an io.MultiWriter, so a failed file write is never counted.
type copyCounter func(int)

func (c copyCounter) Write(p []byte) (int, error) {
	c(len(p))
	return len(p), nil
}

func copyReaderFile(ctx context.Context, builder *fat32.Builder, file fileCopy) (copyResult, error) {
	if parent := path.Dir(file.destination); parent != "." {
		if err := builder.MkdirAll(parent); err != nil {
			return copyResult{}, err
		}
	}
	dst, err := builder.Create(file.destination)
	if err != nil {
		return copyResult{}, err
	}
	h := sha256.New()
	sinks := []io.Writer{dst, h}
	if file.onCopy != nil {
		sinks = append(sinks, copyCounter(file.onCopy))
	}
	limited := io.LimitReader(file.source, int64(file.expectedSize)+1)
	written, copyErr := copyContext(ctx, io.MultiWriter(sinks...), limited)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		return copyResult{written: uint64(written)}, errors.Join(copyErr, closeErr)
	}
	actual := VerificationEntry{Path: file.destination, Size: uint64(written), SHA256: hex.EncodeToString(h.Sum(nil))}
	if !file.matches(actual) {
		return copyResult{written: actual.Size}, fmt.Errorf("%w: %s: got %d bytes", ErrVerification, file.destination, written)
	}
	return copyResult{entry: actual, written: actual.Size}, nil
}

func (f fileCopy) matches(actual VerificationEntry) bool {
	if actual.Size != f.expectedSize {
		return false
	}
	return f.expectedHash == "" || actual.SHA256 == f.expectedHash
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

// newExtentReader reads file's bytes straight out of the ISO extents.
func newExtentReader(source io.ReaderAt, file installeriso.Entry) io.Reader {
	return &extentReader{source: source, extents: append([]installeriso.Extent(nil), file.Extents...), remaining: file.Size}
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
