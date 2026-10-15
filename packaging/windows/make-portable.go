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

// releaseVersion is a version string that matched versionPattern, so it is
// safe to embed in file names and in README-Windows.txt.
type releaseVersion string

func parseReleaseVersion(s string) (releaseVersion, error) {
	if !versionPattern.MatchString(s) {
		return "", errUsage
	}
	return releaseVersion(s), nil
}

// artifactName is the shared base name of the stage directory and the ZIP.
func (v releaseVersion) artifactName() string {
	return "GoFlasher-" + string(v) + "-windows-amd64"
}

// portableFlags is the command line exactly as given, before validation.
type portableFlags struct {
	executable, version, output string
}

// portableLayout is one validated packaging run with every path resolved to
// an absolute path.
type portableLayout struct {
	repo        string // repository root, source of README-Windows.txt
	executable  string // signed GoFlasher executable to ship
	output      string // directory holding the stage tree and the archive
	stageDir    string // tree that becomes the ZIP contents
	archivePath string // the portable ZIP
	version     releaseVersion
}

func main() {
	var f portableFlags
	flag.StringVar(&f.executable, "executable", "", "signed GoFlasher executable")
	flag.StringVar(&f.version, "version", "", "release version")
	flag.StringVar(&f.output, "output", "", "output directory")
	flag.Parse()
	if err := packagePortable(f); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func packagePortable(f portableFlags) error {
	l, err := newPortableLayout(f)
	if err != nil {
		return err
	}
	return l.build()
}

// newPortableLayout rejects the flag combinations no layout can be built from
// and resolves the paths the run will touch.
func newPortableLayout(f portableFlags) (portableLayout, error) {
	if f.executable == "" || f.output == "" {
		return portableLayout{}, errUsage
	}
	version, err := parseReleaseVersion(f.version)
	if err != nil {
		return portableLayout{}, err
	}
	l := portableLayout{version: version}
	for _, p := range []struct {
		dst *string
		src string
	}{
		{&l.repo, filepath.Join("packaging", "windows", "..", "..")},
		{&l.executable, f.executable},
		{&l.output, f.output},
	} {
		if *p.dst, err = filepath.Abs(p.src); err != nil {
			return portableLayout{}, err
		}
	}
	name := version.artifactName()
	l.stageDir, l.archivePath = filepath.Join(l.output, name), filepath.Join(l.output, name+".zip")
	return l, nil
}

func (l portableLayout) build() error {
	for _, step := range []func() error{l.resetArtifacts, l.stage, l.zipStage, l.writeChecksum} {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func (l portableLayout) checksumPath() string {
	return l.archivePath + ".sha256"
}

// resetArtifacts discards any previous run and creates the empty stage tree.
func (l portableLayout) resetArtifacts() error {
	if err := os.RemoveAll(l.stageDir); err != nil {
		return err
	}
	_ = os.Remove(l.archivePath)
	_ = os.Remove(l.checksumPath())
	return os.MkdirAll(l.stageDir, 0755)
}

// stage fills the stage directory with everything the ZIP ships.
// License texts are not staged here: they are embedded in the executable by
// internal/legal and shown under Settings, which is how they reach the user.
// README-Windows.txt stays a file because it explains how to verify the ZIP
// before running the executable, and that has to be readable without running
// it.
func (l portableLayout) stage() error {
	if err := l.stageExecutable(); err != nil {
		return err
	}
	return l.stageReadme()
}

func (l portableLayout) stageExecutable() error {
	in, err := os.Open(l.executable)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(filepath.Join(l.stageDir, "GoFlasher.exe"))
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

// stageReadme copies README-Windows.txt with VERSION substituted.
func (l portableLayout) stageReadme() error {
	readme, err := os.ReadFile(filepath.Join(l.repo, "packaging", "windows", "README-Windows.txt"))
	if err != nil {
		return err
	}
	text := strings.ReplaceAll(string(readme), "VERSION", string(l.version))
	return os.WriteFile(filepath.Join(l.stageDir, "README-Windows.txt"), []byte(text), 0644)
}

func (l portableLayout) zipStage() error {
	f, err := os.Create(l.archivePath)
	if err != nil {
		return err
	}
	b := zipBuilder{zw: zip.NewWriter(f), base: l.output}
	err = filepath.WalkDir(l.stageDir, b.addWalked)
	if closeErr := b.zw.Close(); err == nil {
		err = closeErr
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// writeChecksum writes the sha256sum-style companion file for the archive.
func (l portableLayout) writeChecksum() error {
	h := sha256.New()
	if err := copyInto(h, l.archivePath); err != nil {
		return err
	}
	line := hex.EncodeToString(h.Sum(nil)) + "  " + filepath.Base(l.archivePath) + "\n"
	return os.WriteFile(l.checksumPath(), []byte(line), 0644)
}

// zipBuilder writes deflated entries named relative to base.
type zipBuilder struct {
	zw   *zip.Writer
	base string
}

// addWalked is a filepath.WalkDirFunc that adds every regular file it visits.
func (b zipBuilder) addWalked(path string, d os.DirEntry, walkErr error) error {
	if walkErr != nil || d.IsDir() {
		return walkErr
	}
	rel, err := filepath.Rel(b.base, path)
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
	w, err := b.zw.CreateHeader(h)
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
