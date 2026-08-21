//go:build linux || darwin

package wim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"

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

var openNative = func(path, root string) (nativeLibrary, error) {
	library, err := native.Open(path, root)
	if err != nil {
		return nil, err
	}
	return libraryAdapter{library}, nil
}
var locateBundledLibrary = bundledLibraryPath

func backendProbe() (err error) {
	libraryPath, libraryRoot, err := locateBundledLibrary()
	if err != nil {
		return err
	}
	lib, err := openNative(libraryPath, libraryRoot)
	if err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	return lib.Close()
}

func backendSplit(ctx context.Context, sourcePath, outputDir string, partSize uint64, progress ProgressFunc) (parts []Part, err error) {
	libraryPath, libraryRoot, err := locateBundledLibrary()
	if err != nil {
		return nil, err
	}
	lib, err := openNative(libraryPath, libraryRoot)
	if err != nil {
		return nil, errors.Join(ErrUnsupported, err)
	}
	defer func() { err = errors.Join(err, lib.Close()) }()
	image, err := lib.OpenWIM(sourcePath)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, image.Close()) }()
	if progress != nil {
		progress(0, 1)
	}
	if err := image.Split(filepath.Join(outputDir, "install.swm"), partSize); err != nil {
		removeParts(outputDir)
		return nil, err
	}
	parts, err = discoverParts(outputDir)
	if err != nil {
		removeParts(outputDir)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		removeParts(outputDir)
		return nil, err
	}
	if progress != nil {
		progress(1, 1)
	}
	return parts, nil
}

func bundledLibraryPath() (string, string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", "", errors.Join(ErrUnsupported, err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", "", errors.Join(ErrUnsupported, err)
	}
	return bundledLibraryPathFor(executable, runtime.GOOS)
}

func bundledLibraryPathFor(executable, goos string) (string, string, error) {
	if goos == "darwin" {
		root := path.Dir(executable)
		contents := path.Dir(root)
		if path.Base(root) != "MacOS" || path.Base(contents) != "Contents" {
			return "", "", fmt.Errorf("%w: executable is not in a macOS application bundle", ErrUnsupported)
		}
		return path.Join(contents, "Frameworks", "libwim.15.dylib"), contents, nil
	}
	if goos != "linux" {
		return "", "", fmt.Errorf("%w: bundled libwim is only supported on Linux and macOS", ErrUnsupported)
	}
	if path.Clean(executable) == "/usr/bin/goflasher" {
		root := path.Join("/usr/lib/goflasher/lib/wimlib", native.BundledVersion)
		return path.Join(root, "libwim.so.15"), "/usr/lib/goflasher", nil
	}
	root := path.Dir(executable)
	return path.Join(root, "lib", "wimlib", native.BundledVersion, "libwim.so.15"), root, nil
}
