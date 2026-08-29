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
	root, err := windowsSystemRoot()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, "System32", "dism.exe"))
	if err != nil {
		return "", errors.Join(ErrUnsupported, err)
	}
	if !filepath.IsAbs(resolved) {
		return "", fmt.Errorf("%w: DISM path is not absolute", ErrUnsupported)
	}
	if !withinDirectory(root, resolved) {
		return "", fmt.Errorf("%w: DISM resolves outside the Windows directory", ErrUnsupported)
	}
	if err := requireRegularFile(resolved); err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// windowsSystemRoot resolves the Windows directory named by the environment,
// rejecting a missing or relative one.
func windowsSystemRoot() (string, error) {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = os.Getenv("WINDIR")
	}
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: Windows system directory is unavailable", ErrUnsupported)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", errors.Join(ErrUnsupported, err)
	}
	return resolved, nil
}

// withinDirectory reports whether path is root itself or lives beneath it.
// Both arguments must already be symlink-resolved.
func withinDirectory(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// requireRegularFile rejects a directory, device, or anything else DISM must
// not be.
func requireRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	if !info.Mode().IsRegular() {
		return errors.Join(ErrUnsupported, errors.New("DISM is not a regular file"))
	}
	return nil
}

func backendProbe() error {
	_, err := resolveDISM()
	if err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	return nil
}

func backendSplit(ctx context.Context, req splitRequest) ([]Part, error) {
	sizeMiB := req.partSize / (1024 * 1024)
	if sizeMiB == 0 {
		return nil, fmt.Errorf("%w: part size is smaller than one MiB", ErrUnsupported)
	}
	dismPath, err := resolveDISM()
	if err != nil {
		return nil, errors.Join(ErrUnsupported, err)
	}
	req.report(0, 1)
	output, runErr := executeDISM(ctx, dismPath, splitImageArgs(req.sourcePath, req.outputDir, sizeMiB))
	if runErr != nil {
		removeParts(req.outputDir)
		return nil, dismFailure(ctx, output, runErr)
	}
	parts, err := collectDISMParts(ctx, req.outputDir)
	if err != nil {
		return nil, err
	}
	req.report(1, 1)
	return parts, nil
}

// splitImageArgs builds the DISM command line, one exact argument per element
// so no shell ever parses these paths.
func splitImageArgs(sourcePath, outputDir string, sizeMiB uint64) []string {
	return []string{
		"/English",
		"/Split-Image",
		"/ImageFile:" + sourcePath,
		"/SWMFile:" + filepath.Join(outputDir, "install.swm"),
		"/FileSize:" + strconv.FormatUint(sizeMiB, 10),
	}
}

// dismFailure reports cancellation in preference to DISM's own error, and
// otherwise attaches at most the tail of DISM's diagnostic output.
func dismFailure(ctx context.Context, output []byte, runErr error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	diagnostic := strings.TrimSpace(string(output))
	if len(diagnostic) > maxDISMDiagnostic {
		diagnostic = diagnostic[len(diagnostic)-maxDISMDiagnostic:]
	}
	if diagnostic != "" {
		return fmt.Errorf("DISM Split-Image failed: %w: %s", runErr, diagnostic)
	}
	return fmt.Errorf("DISM Split-Image failed: %w", runErr)
}

// collectDISMParts validates what DISM wrote, removing the output unless it is
// a complete set of parts the caller still wants.
func collectDISMParts(ctx context.Context, outputDir string) ([]Part, error) {
	parts, err := discoverParts(outputDir)
	if err == nil && len(parts) == 0 {
		err = fmt.Errorf("%w: DISM produced no split parts", ErrUnsupported)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		removeParts(outputDir)
		return nil, err
	}
	return parts, nil
}
