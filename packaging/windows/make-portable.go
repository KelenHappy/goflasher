// Command make-portable creates the permanent Windows portable ZIP layout.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^v?[0-9A-Za-z][0-9A-Za-z._-]*$`)

var errUsage = errors.New("usage: go run ./packaging/windows --executable EXE --version VERSION --output DIR")

func main() {
	executable := flag.String("executable", "", "signed GoFlasher executable")
	version := flag.String("version", "", "release version")
	output := flag.String("output", "", "output directory")
	flag.Parse()
	if err := packagePortable(*executable, *version, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func packagePortable(executable, version, output string) error {
	if err := checkFlags(executable, version, output); err != nil {
		return err
	}
	repo, err := filepath.Abs(filepath.Join("packaging", "windows", "..", ".."))
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	name := "GoFlasher-" + version + "-windows-amd64"
	stage, archive := filepath.Join(output, name), filepath.Join(output, name+".zip")
	if err := resetArtifacts(stage, archive); err != nil {
		return err
	}
	if err := stageLayout(repo, stage, executable, version); err != nil {
		return err
	}
	if err := zipTree(output, stage, archive); err != nil {
		return err
	}
	return writeChecksum(archive)
}

// checkFlags rejects the flag combinations no layout can be built from.
func checkFlags(executable, version, output string) error {
	if executable == "" || output == "" {
		return errUsage
	}
	if !versionPattern.MatchString(version) {
		return errUsage
	}
	return nil
}

// resetArtifacts discards any previous run and creates the empty stage tree.
func resetArtifacts(stage, archive string) error {
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	_ = os.Remove(archive)
	_ = os.Remove(archive + ".sha256")
	return os.MkdirAll(stage, 0755)
}

// stageLayout fills the stage directory with everything the ZIP ships.
//
// License texts are not staged here: they are embedded in the executable by
// internal/legal and shown under Settings, which is how they reach the user.
// README-Windows.txt stays a file because it explains how to verify the ZIP
// before running the executable, and that has to be readable without running
// it.
func stageLayout(repo, stage, executable, version string) error {
	if err := copyFile(executable, filepath.Join(stage, "GoFlasher.exe")); err != nil {
		return err
	}
	return stageReadme(repo, stage, version)
}

// stageReadme copies README-Windows.txt with VERSION substituted.
func stageReadme(repo, stage, version string) error {
	readme, err := os.ReadFile(filepath.Join(repo, "packaging", "windows", "README-Windows.txt"))
	if err != nil {
		return err
	}
	text := strings.ReplaceAll(string(readme), "VERSION", version)
	return os.WriteFile(filepath.Join(stage, "README-Windows.txt"), []byte(text), 0644)
}

// writeChecksum writes the sha256sum-style companion file for the archive.
func writeChecksum(archive string) error {
	sum, err := fileSHA256(archive)
	if err != nil {
		return err
	}
	line := sum + "  " + filepath.Base(archive) + "\n"
	return os.WriteFile(archive+".sha256", []byte(line), 0644)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	closeErr := f.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func zipTree(base, stage, destination string) error {
	f, err := os.Create(destination)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	err = filepath.WalkDir(stage, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		return addZipEntry(zw, base, path, d)
	})
	if closeErr := zw.Close(); err == nil {
		err = closeErr
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// addZipEntry writes one deflated file, named relative to base.
func addZipEntry(zw *zip.Writer, base, path string, d os.DirEntry) error {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return err
	}
	info, err := d.Info()
	if err != nil {
		return err
	}
	h, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	h.Name = filepath.ToSlash(rel)
	h.Method = zip.Deflate
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	return copyInto(w, path)
}

// copyInto streams the file at path into w and closes the source.
func copyInto(w io.Writer, path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(w, src)
	closeErr := src.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
