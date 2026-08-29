// Command make-portable creates the permanent Windows portable ZIP layout.
package main

import (
	"archive/zip"
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	versionPattern     = regexp.MustCompile(`^v?[0-9A-Za-z][0-9A-Za-z._-]*$`)
	licenseNamePattern = regexp.MustCompile(`^(LICENSE|COPYING|NOTICE)(\..*)?$`)
	unsafeNameChars    = regexp.MustCompile(`[^A-Za-z0-9._-]`)
)

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
	switch {
	case executable == "", output == "":
		return errUsage
	case !versionPattern.MatchString(version):
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
	return os.MkdirAll(filepath.Join(stage, "licenses"), 0o755)
}

// stageLayout fills the stage directory with everything the ZIP ships.
func stageLayout(repo, stage, executable, version string) error {
	if err := copyFile(executable, filepath.Join(stage, "GoFlasher.exe")); err != nil {
		return err
	}
	for _, notice := range []string{"THIRD_PARTY_NOTICES.md", "THIRD_PARTY_NOTICES.zh-TW.md"} {
		if err := copyFile(filepath.Join(repo, "docs", "legal", notice), filepath.Join(stage, notice)); err != nil {
			return err
		}
	}
	if err := stageReadme(repo, stage, version); err != nil {
		return err
	}
	return collectLicenses(repo, stage)
}

// stageReadme copies README-Windows.txt with VERSION substituted.
func stageReadme(repo, stage, version string) error {
	readme, err := os.ReadFile(filepath.Join(repo, "packaging", "windows", "README-Windows.txt"))
	if err != nil {
		return err
	}
	text := strings.ReplaceAll(string(readme), "VERSION", version)
	return os.WriteFile(filepath.Join(stage, "README-Windows.txt"), []byte(text), 0o644)
}

// writeChecksum writes the sha256sum-style companion file for the archive.
func writeChecksum(archive string) error {
	sum, err := fileSHA256(archive)
	if err != nil {
		return err
	}
	line := sum + "  " + filepath.Base(archive) + "\n"
	return os.WriteFile(archive+".sha256", []byte(line), 0o644)
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

func collectLicenses(repo, stage string) error {
	modules, err := compiledModuleDirs(repo)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(modules))
	for path := range modules {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, module := range paths {
		if err := copyModuleLicenses(module, modules[module], filepath.Join(stage, "licenses")); err != nil {
			return err
		}
	}
	return nil
}

// compiledModuleDirs maps every module compiled into the GUI binary to its
// source directory.
func compiledModuleDirs(repo string) (map[string]string, error) {
	cmd := exec.Command("go", "list", "-deps", "-tags", "fyne", "-f", `{{with .Module}}{{if .Dir}}{{.Path}}|{{.Dir}}{{end}}{{end}}`, "./cmd/usbwriter")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list compiled modules: %w", err)
	}
	modules := make(map[string]string)
	scan := bufio.NewScanner(strings.NewReader(string(out)))
	for scan.Scan() {
		parts := strings.SplitN(scan.Text(), "|", 2)
		if len(parts) == 2 {
			modules[parts[0]] = parts[1]
		}
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	return modules, nil
}

// copyModuleLicenses copies each root license file of module into licenses and
// fails when the module ships none.
func copyModuleLicenses(module, dir, licenses string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	safe := unsafeNameChars.ReplaceAllString(module, "_")
	found := false
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !licenseNamePattern.MatchString(entry.Name()) {
			continue
		}
		found = true
		if err := copyFile(filepath.Join(dir, entry.Name()), filepath.Join(licenses, safe+"_"+entry.Name())); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("compiled module %s has no root license file", module)
	}
	return nil
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
