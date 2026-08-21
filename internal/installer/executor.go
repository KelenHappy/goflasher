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
	if plan == nil || source == nil || target == nil || len(plan.planned) == 0 {
		return result, fmt.Errorf("%w: invalid execution input", ErrIncomplete)
	}
	if err := ctx.Err(); err != nil {
		return result, errors.Join(ErrIncomplete, err)
	}
	if plan.strategy == SplitWIM && x.Splitter == nil {
		return result, errors.Join(ErrIncomplete, ErrSplitterRequired)
	}
	if plan.strategy == SplitWIM {
		if preflight, ok := x.Splitter.(interface{ Preflight(context.Context) error }); ok {
			if err := preflight.Preflight(ctx); err != nil {
				return result, errors.Join(ErrIncomplete, err)
			}
		}
	}
	totalLBAs := (plan.esp.StartOffset + plan.esp.Size + 33*logicalSectorSize) / logicalSectorSize
	layout, err := gpt.Build(totalLBAs, logicalSectorSize, nil)
	if err != nil {
		return result, errors.Join(ErrIncomplete, err)
	}
	if layout.PartitionStartLBA*logicalSectorSize != plan.esp.StartOffset || (layout.PartitionEndLBA-layout.PartitionStartLBA+1)*logicalSectorSize != plan.esp.Size {
		return result, fmt.Errorf("%w: GPT layout differs from preflight", ErrIncomplete)
	}
	if err := layout.WriteTo(target); err != nil {
		return result, errors.Join(ErrIncomplete, err)
	}
	esp, err := gpt.NewPartitionWriterAt(target, layout.PartitionStartLBA, layout.PartitionEndLBA, logicalSectorSize)
	if err != nil {
		return result, errors.Join(ErrIncomplete, err)
	}
	builder, err := fat32.NewBuilder(ctx, esp, plan.esp.Size, "GOFLASHER")
	if err != nil {
		return result, errors.Join(ErrIncomplete, err)
	}
	for _, item := range plan.planned {
		if item.source.Type == installeriso.Directory {
			if err := builder.MkdirAll(item.destination); err != nil {
				return result, errors.Join(ErrIncomplete, err)
			}
		}
	}
	for _, item := range plan.planned {
		if item.source.Type != installeriso.File || plan.strategy == SplitWIM && strings.EqualFold(item.source.Path, "sources/install.wim") {
			continue
		}
		verified, n, err := copyISOFile(ctx, builder, source, plan, item)
		result.BytesWritten += n
		if err != nil {
			return result, errors.Join(ErrIncomplete, err)
		}
		result.VerificationManifest = append(result.VerificationManifest, verified)
	}
	if plan.strategy == SplitWIM {
		wim, ok := plannedBySource(plan, "sources/install.wim")
		if !ok {
			return result, fmt.Errorf("%w: planned WIM is missing", ErrIncomplete)
		}
		reader := newExtentReader(source, wim.source.Extents, wim.source.Size)
		expectedWIMHash := verificationHash(plan, wim.destination, wim.source.Size)
		if expectedWIMHash == "" {
			return result, fmt.Errorf("%w: planned WIM hash is missing", ErrVerification)
		}
		nextPart := 1
		err = x.Splitter.Split(ctx, reader, wim.source.Size, expectedWIMHash, plan.splitSize, func(part SplitPart) error {
			if nextPart > plan.splitParts {
				return fmt.Errorf("%w: split pipeline produced too many parts", ErrVerification)
			}
			want := "install.swm"
			if nextPart > 1 {
				want = "install" + strconv.Itoa(nextPart) + ".swm"
			}
			if part.Name != want || part.Data == nil || part.Size == 0 || part.Size > plan.splitSize {
				return fmt.Errorf("%w: invalid split part %q", ErrVerification, part.Name)
			}
			destination := path.Join(path.Dir(wim.destination), want)
			verified, n, copyErr := copyReaderFile(ctx, builder, destination, part.Data, part.Size, "")
			result.BytesWritten += n
			if copyErr != nil {
				return copyErr
			}
			result.VerificationManifest = append(result.VerificationManifest, verified)
			nextPart++
			return nil
		})
		if err != nil {
			return result, errors.Join(ErrIncomplete, ErrWIMSplitFailure, err)
		}
		if nextPart != plan.splitParts+1 {
			return result, fmt.Errorf("%w: split pipeline produced %d of %d planned parts", ErrIncomplete, nextPart-1, plan.splitParts)
		}
	}
	if err := builder.Sync(); err != nil {
		return result, errors.Join(ErrIncomplete, err)
	}
	if err := target.Sync(); err != nil {
		return result, errors.Join(ErrIncomplete, err)
	}
	result.Complete = true
	return result, nil
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
	return copyReaderFile(ctx, builder, item.destination, reader, item.source.Size, expected)
}

func verificationHash(plan *BuildPlan, destination string, size uint64) string {
	for _, verification := range plan.verification {
		if strings.EqualFold(strings.ReplaceAll(verification.Path, "\\", "/"), destination) && verification.Size == size {
			return verification.SHA256
		}
	}
	return ""
}

func copyReaderFile(ctx context.Context, builder *fat32.Builder, destination string, source io.Reader, expectedSize uint64, expectedHash string) (VerificationEntry, uint64, error) {
	if parent := path.Dir(destination); parent != "." {
		if err := builder.MkdirAll(parent); err != nil {
			return VerificationEntry{}, 0, err
		}
	}
	dst, err := builder.Create(destination)
	if err != nil {
		return VerificationEntry{}, 0, err
	}
	h := sha256.New()
	limited := io.LimitReader(source, int64(expectedSize)+1)
	written, copyErr := copyContext(ctx, io.MultiWriter(dst, h), limited)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		return VerificationEntry{}, uint64(written), errors.Join(copyErr, closeErr)
	}
	actualHash := hex.EncodeToString(h.Sum(nil))
	if uint64(written) != expectedSize || expectedHash != "" && actualHash != expectedHash {
		return VerificationEntry{}, uint64(written), fmt.Errorf("%w: %s: got %d bytes", ErrVerification, destination, written)
	}
	return VerificationEntry{Path: destination, Size: uint64(written), SHA256: actualHash}, uint64(written), nil
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, copyBufferSize)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			written, writeErr := dst.Write(buf[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
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
