// Package wim provides a handle-free Go API around the bundled libwim.
package wim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/goflasher/goflasher/internal/wim/native"
)

type ProgressFunc func(completed, total uint64)

type Part struct {
	Path string
	Size uint64
}

var ErrUnsupported = errors.New("WIM splitting is unsupported")

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

// locateBundledLibrary is replaceable only by package tests. Production keeps
// the platform-specific, application-controlled path policy in
// bundledLibraryPath.
var locateBundledLibrary = bundledLibraryPath

// Probe loads, validates, initializes, and closes the bundled library without
// opening a WIM. Callers use it during preflight, before opening a target.
func Probe() (err error) {
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

// Split uses the fixed bundled libwim and never exposes a WIMStruct or dynamic
// library handle. Progress callbacks run synchronously on the calling Go
// goroutine; no Go callback is passed to native code or invoked on its threads.
func Split(ctx context.Context, sourcePath, outputDir string, partSize uint64, progress ProgressFunc) (parts []Part, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if partSize == 0 {
		return nil, fmt.Errorf("%w: invalid part size", ErrUnsupported)
	}
	sourcePath, err = canonicalAbsolute(sourcePath)
	if err != nil {
		return nil, err
	}
	outputDir, err = canonicalAbsolute(outputDir)
	if err != nil {
		return nil, err
	}
	if err := rejectExistingParts(outputDir); err != nil {
		return nil, err
	}
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
	firstPart := filepath.Join(outputDir, "install.swm")
	if err := image.Split(firstPart, partSize); err != nil {
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
		// Only accept the canonical application-bundle layout. In particular,
		// never fall back to the working directory, DYLD_LIBRARY_PATH, or a
		// system libwim when the signed nested library is absent.
		contents := path.Dir(root)
		if path.Base(root) != "MacOS" || path.Base(contents) != "Contents" {
			return "", "", fmt.Errorf("%w: executable is not in a macOS application bundle", ErrUnsupported)
		}
		return path.Join(contents, "Frameworks", "libwim.15.dylib"), contents, nil
	}
	if goos == "linux" {
		if path.Clean(executable) == "/usr/bin/goflasher" {
			root := path.Join("/usr/lib/goflasher/lib/wimlib", native.BundledVersion)
			return path.Join(root, "libwim.so.15"), "/usr/lib/goflasher", nil
		}
		root := path.Dir(executable)
		return path.Join(root, "lib", "wimlib", native.BundledVersion, "libwim.so.15"), root, nil
	}
	root := filepath.Dir(executable)
	name := "libwim.so.15"
	if goos == "windows" {
		name = "libwim-15.dll"
	}
	return filepath.Join(root, "lib", "wimlib", native.BundledVersion, name), root, nil
}

func canonicalAbsolute(name string) (string, error) {
	if !filepath.IsAbs(name) {
		return "", fmt.Errorf("%w: path must be absolute", ErrUnsupported)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(name))
	if err != nil {
		return "", errors.Join(ErrUnsupported, err)
	}
	return resolved, nil
}

func rejectExistingParts(dir string) error {
	parts, err := discoverParts(dir)
	if err != nil {
		return err
	}
	if len(parts) != 0 {
		return fmt.Errorf("%w: output directory already contains install*.swm", ErrUnsupported)
	}
	return nil
}

func discoverParts(dir string) ([]Part, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	indexed := map[int]Part{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lowerName := strings.ToLower(entry.Name())
		n, ok := partNumber(lowerName)
		if !ok {
			if strings.HasPrefix(lowerName, "install") && strings.HasSuffix(lowerName, ".swm") {
				return nil, fmt.Errorf("%w: unexpected split output %q", ErrUnsupported, entry.Name())
			}
			continue
		}
		if entry.Name() != lowerName {
			return nil, fmt.Errorf("%w: non-canonical split output %q", ErrUnsupported, entry.Name())
		}
		if _, duplicate := indexed[n]; duplicate {
			return nil, fmt.Errorf("%w: duplicate split output %d", ErrUnsupported, n)
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if info.Size() <= 0 {
			return nil, fmt.Errorf("%w: empty split part", ErrUnsupported)
		}
		indexed[n] = Part{Path: filepath.Join(dir, entry.Name()), Size: uint64(info.Size())}
	}
	parts := make([]Part, 0, len(indexed))
	for n := 1; n <= len(indexed); n++ {
		part, ok := indexed[n]
		if !ok {
			return nil, fmt.Errorf("%w: non-contiguous split output", ErrUnsupported)
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func partNumber(name string) (int, bool) {
	if name == "install.swm" {
		return 1, true
	}
	if !strings.HasPrefix(name, "install") || !strings.HasSuffix(name, ".swm") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "install"), ".swm"))
	return n, err == nil && n >= 2 && name == "install"+strconv.Itoa(n)+".swm"
}

func removeParts(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if _, ok := partNumber(strings.ToLower(entry.Name())); ok && !entry.IsDir() {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
