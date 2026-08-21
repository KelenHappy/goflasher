//go:build windows

package wim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const maxDISMDiagnostic = 32 * 1024

var resolveDISM = trustedDISMPath
var executeDISM = func(ctx context.Context, executable string, args []string) ([]byte, error) {
	return exec.CommandContext(ctx, executable, args...).CombinedOutput()
}

func trustedDISMPath() (string, error) {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = os.Getenv("WINDIR")
	}
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: Windows system directory is unavailable", ErrUnsupported)
	}
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", errors.Join(ErrUnsupported, err)
	}
	candidate := filepath.Join(resolvedRoot, "System32", "dism.exe")
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", errors.Join(ErrUnsupported, err)
	}
	if !filepath.IsAbs(resolved) {
		return "", fmt.Errorf("%w: DISM path is not absolute", ErrUnsupported)
	}
	relative, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: DISM resolves outside the Windows directory", ErrUnsupported)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("DISM is not a regular file")
		}
		return "", errors.Join(ErrUnsupported, err)
	}
	return filepath.Clean(resolved), nil
}

func backendProbe() error {
	_, err := resolveDISM()
	if err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	return nil
}

func backendSplit(ctx context.Context, sourcePath, outputDir string, partSize uint64, progress ProgressFunc) ([]Part, error) {
	sizeMiB := partSize / (1024 * 1024)
	if sizeMiB == 0 {
		return nil, fmt.Errorf("%w: part size is smaller than one MiB", ErrUnsupported)
	}
	dismPath, err := resolveDISM()
	if err != nil {
		return nil, errors.Join(ErrUnsupported, err)
	}
	firstPart := filepath.Join(outputDir, "install.swm")
	args := []string{
		"/English",
		"/Split-Image",
		"/ImageFile:" + sourcePath,
		"/SWMFile:" + firstPart,
		"/FileSize:" + strconv.FormatUint(sizeMiB, 10),
	}
	if progress != nil {
		progress(0, 1)
	}
	output, runErr := executeDISM(ctx, dismPath, args)
	if runErr != nil {
		removeParts(outputDir)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		diagnostic := strings.TrimSpace(string(output))
		if len(diagnostic) > maxDISMDiagnostic {
			diagnostic = diagnostic[len(diagnostic)-maxDISMDiagnostic:]
		}
		if diagnostic != "" {
			return nil, fmt.Errorf("DISM Split-Image failed: %w: %s", runErr, diagnostic)
		}
		return nil, fmt.Errorf("DISM Split-Image failed: %w", runErr)
	}
	parts, err := discoverParts(outputDir)
	if err != nil || len(parts) == 0 {
		removeParts(outputDir)
		if err == nil {
			err = fmt.Errorf("%w: DISM produced no split parts", ErrUnsupported)
		}
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
