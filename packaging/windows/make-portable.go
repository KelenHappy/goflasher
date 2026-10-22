// Command make-portable creates the permanent Windows portable ZIP layout.
package main

import (
	"archive/zip"
	"bytes"
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

var errUsage = errors.New("usage: go run ./packaging/windows --executable EXE --version VERSION --output DIR [--wimlib-record DIR]")

// wimlibMarker is the linkage constant in internal/wim/native/link_wimlib.go,
// compiled in only with the wimlib build tag; packaging/legal/links-wimlib.sh
// searches for the same text.
var wimlibMarker = []byte("statically linked wimlib")

// wimlibRecordItems are the compliance record files shipped beside the
// executable, matching packaging/legal/install-wimlib-record.sh.
var wimlibRecordItems = []string{"source.sha256", "artifact.sha256", "build-metadata.txt", "dependencies.txt", "license-inventory.txt", "corresponding-source.txt", "LEGAL_APPROVED"}

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
	executable, version, output, wimlibRecord string
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
	// wimlibRecord is the approved wimlib compliance record, or empty when
	// none was given. It is required when the executable links wimlib.
	wimlibRecord string
}

func main() {
	var f portableFlags
	flag.StringVar(&f.executable, "executable", "", "signed GoFlasher executable")
	flag.StringVar(&f.version, "version", "", "release version")
	flag.StringVar(&f.output, "output", "", "output directory")
	flag.StringVar(&f.wimlibRecord, "wimlib-record", "", "approved wimlib compliance record, required when the executable links wimlib")
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
	paths := []struct {
		dst *string
		src string
	}{
		{&l.repo, filepath.Join("packaging", "windows", "..", "..")},
		{&l.executable, f.executable},
		{&l.output, f.output},
	}
	if f.wimlibRecord != "" {
		paths = append(paths, struct {
			dst *string
			src string
		}{&l.wimlibRecord, f.wimlibRecord})
	}
	for _, p := range paths {
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
	for _, step := range []func() error{l.stageExecutable, l.stageReadme, l.stageWimlibRecord} {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// stageWimlibRecord ships wimlib's licenses and compliance record under
// legal/wimlib-1.14.5 when the executable links wimlib, and refuses to
// package such an executable without the record.
func (l portableLayout) stageWimlibRecord() error {
	exe, err := os.ReadFile(l.executable)
	if err != nil {
		return err
	}
	if !bytes.Contains(exe, wimlibMarker) {
		return nil
	}
	if l.wimlibRecord == "" {
		return errors.New("GoFlasher.exe links wimlib; --wimlib-record is required")
	}
	record := copySpec{from: l.wimlibRecord, to: filepath.Join(l.stageDir, "legal", "wimlib-1.14.5")}
	if err := copyTree(record.child("licenses")); err != nil {
		return err
	}
	for _, item := range wimlibRecordItems {
		if err := copyFile(record.child(item)); err != nil {
			return err
		}
	}
	return nil
}

// copySpec names the source and destination of one copy, so the two paths
// cannot be swapped by argument order.
type copySpec struct {
	from, to string
}

// child is the copy of the entry called name inside from to the same name
// inside to.
func (c copySpec) child(name string) copySpec {
	return copySpec{from: filepath.Join(c.from, name), to: filepath.Join(c.to, name)}
}

// copyTree copies every regular file under c.from to the same relative path
// under c.to.
func copyTree(c copySpec) error {
	return filepath.WalkDir(c.from, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(c.from, path)
		if err != nil {
			return err
		}
		return copyFile(c.child(rel))
	})
}

// copyFile copies c.from to c.to, creating the destination's parent
// directories.
func copyFile(c copySpec) error {
	if err := os.MkdirAll(filepath.Dir(c.to), 0755); err != nil {
		return err
	}
	out, err := os.Create(c.to)
	if err != nil {
		return err
	}
	copyErr := copyInto(out, c.from)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (l portableLayout) stageExecutable() error {
	return copyFile(copySpec{from: l.executable, to: filepath.Join(l.stageDir, "GoFlasher.exe")})
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
