//go:build linux || darwin

package wim

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/goflasher/goflasher/internal/wim/native"
)

type nativeImage interface {
	Split(string, uint64) error
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
	req.report(0, 1)
	if err := image.Split(filepath.Join(req.OutputDir, "install.swm"), req.PartSize); err != nil {
		removeParts(req.OutputDir)
		return nil, err
	}
	parts, err = discoverParts(req.OutputDir)
	if err != nil {
		removeParts(req.OutputDir)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		removeParts(req.OutputDir)
		return nil, err
	}
	req.report(1, 1)
	return parts, nil
}
