// Package wim splits Windows images using the platform backend: trusted system
// DISM on Windows and GoFlasher's bundled libwim on Linux and macOS.
package wim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ProgressFunc func(completed, total uint64)

type Part struct {
	Path string
	Size uint64
}

var ErrUnsupported = errors.New("WIM splitting is unsupported")

// Probe verifies that the compile-time-selected backend is available.
func Probe() error { return backendProbe() }

// Split canonicalizes and validates its paths before invoking the
// compile-time-selected platform backend.
func Split(ctx context.Context, sourcePath, outputDir string, partSize uint64, progress ProgressFunc) ([]Part, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if partSize == 0 {
		return nil, fmt.Errorf("%w: invalid part size", ErrUnsupported)
	}
	var err error
	if sourcePath, err = canonicalAbsolute(sourcePath); err != nil {
		return nil, err
	}
	if outputDir, err = canonicalAbsolute(outputDir); err != nil {
		return nil, err
	}
	if err := rejectExistingParts(outputDir); err != nil {
		return nil, err
	}
	return backendSplit(ctx, sourcePath, outputDir, partSize, progress)
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
