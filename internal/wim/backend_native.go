package wim

import (
	"context"
	"errors"

	"github.com/goflasher/goflasher/internal/wim/native"
)

type nativeImage interface {
	Split(string, uint64, native.SplitProgress) error
	Close() error
}
type nativeLibrary interface {
	OpenWIM(string) (nativeImage, error)
	Close() error
}

type libraryAdapter struct{ library *native.Library }

func (a libraryAdapter) OpenWIM(path string) (nativeImage, error) { return a.library.OpenWIM(path) }
func (a libraryAdapter) Close() error                             { return a.library.Close() }

var openNative = func() (nativeLibrary, error) {
	library, err := native.Open()
	if err != nil {
		return nil, err
	}
	return libraryAdapter{library}, nil
}

func backendProbe() (err error) {
	lib, err := openNative()
	if err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	return lib.Close()
}

func backendSplit(ctx context.Context, req Request) (parts []Part, err error) {
	lib, err := openNative()
	if err != nil {
		return nil, errors.Join(ErrUnsupported, err)
	}
	defer func() { err = errors.Join(err, lib.Close()) }()
	image, err := lib.OpenWIM(req.SourcePath)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, image.Close()) }()
	// wimlib calls progress synchronously; returning false aborts the split,
	// so cancellation takes effect between progress messages.
	progress := func(done, total uint64) bool {
		req.report(done, total)
		return ctx.Err() == nil
	}
	output := partDir(req.OutputDir)
	if err := image.Split(output.firstPart(), req.PartSize, progress); err != nil {
		output.remove()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	parts, err = output.discover()
	if err != nil {
		output.remove()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		output.remove()
		return nil, err
	}
	return parts, nil
}
