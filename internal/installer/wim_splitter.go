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

	"github.com/goflasher/goflasher/internal/wim"
)

type wimSplitFunc func(context.Context, string, string, uint64, wim.ProgressFunc) ([]wim.Part, error)

// NativeWIMSplitter stages only install.wim in a private temporary directory,
// invokes the bundled libwim, validates its complete output set, and streams
// each part to the executor. Native handles never escape internal/wim.
type NativeWIMSplitter struct {
	split wimSplitFunc
	probe func() error
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

func (s *NativeWIMSplitter) Split(ctx context.Context, source io.Reader, sourceSize uint64, expectedSHA256 string, partSize uint64, emit func(SplitPart) error) (err error) {
	if s == nil || s.split == nil || source == nil || emit == nil || sourceSize == 0 || expectedSHA256 == "" || partSize == 0 || partSize >= maxFATFileSize {
		return fmt.Errorf("%w: invalid native split input", ErrVerification)
	}
	temporary, err := os.MkdirTemp("", "goflasher-wim-*")
	if err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0700); err != nil {
		_ = os.RemoveAll(temporary)
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(temporary)) }()
	sourcePath := filepath.Join(temporary, "install.wim")
	if err := stageWIM(ctx, sourcePath, source, sourceSize, expectedSHA256); err != nil {
		return err
	}
	output := filepath.Join(temporary, "split")
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	parts, err := s.split(ctx, sourcePath, output, partSize, nil)
	if err != nil { // A native call may return only after cancellation; never emit afterwards.
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSplitParts(parts, output, sourceSize, partSize); err != nil {
		return err
	}
	for index, part := range parts {
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
	var total uint64
	for index, part := range parts {
		want := "install.swm"
		if index > 0 {
			want = fmt.Sprintf("install%d.swm", index+1)
		}
		canonical, err := filepath.EvalSymlinks(part.Path)
		if err != nil || filepath.Dir(canonical) != output || filepath.Base(canonical) != want || part.Size == 0 || part.Size > policy || part.Size >= maxFATFileSize {
			return fmt.Errorf("%w: invalid split part %q", ErrVerification, part.Path)
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
