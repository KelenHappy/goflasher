// Package osinfo names the operating system an ISO image carries: Fedora,
// Ubuntu, Windows 11 and so on, with version, architecture and, for Windows,
// the editions inside install.wim.
//
// Everything it reports is read from the untrusted image and is for display
// only. It never decides how an image is written; internal/image's structural
// classification does that.
package osinfo

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	installeriso "github.com/goflasher/goflasher/internal/installer/iso"
)

type Family string

const (
	Unknown Family = ""
	Windows Family = "windows"
	Linux   Family = "linux"
)

// System describes the operating system found on an image. Fields the image
// does not reveal are empty.
type System struct {
	Family   Family
	Name     string // "Fedora Workstation", "Ubuntu", "Windows 11"
	Version  string // "44", "24.04.1 LTS", "24H2"
	Build    string // Windows build, "26100.1742"
	Arch     string // "x86_64", "arm64", "x86"
	Language string // Windows default language, "zh-TW"
	Editions []string
	Label    string // ISO volume label
}

// Known reports whether anything identifying was found.
func (s System) Known() bool { return s.Family != Unknown || s.Name != "" }

// Summary is a one-line, language-neutral description such as
// "Fedora Workstation 44 (x86_64)" or "Windows 11 24H2 (build 26100.1742, x86_64, zh-TW)".
func (s System) Summary() string {
	name := s.Name
	if name == "" {
		name = s.Label
	}
	head := strings.TrimSpace(name + " " + s.Version)
	var details []string
	if s.Build != "" {
		details = append(details, "build "+s.Build)
	}
	for _, d := range []string{s.Arch, s.Language} {
		if d != "" {
			details = append(details, d)
		}
	}
	if len(details) == 0 {
		return head
	}
	return fmt.Sprintf("%s (%s)", head, strings.Join(details, ", "))
}

// IdentifyFile identifies the uncompressed ISO image at path.
func IdentifyFile(path string) (System, error) {
	f, err := os.Open(path)
	if err != nil {
		return System{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return System{}, err
	}
	return IdentifyISO(f, st.Size())
}

// IdentifyISO identifies an uncompressed ISO image. It reads the directory
// tree and a few small files; for Windows it also reads install.wim's header
// and XML description, never its payload.
func IdentifyISO(r io.ReaderAt, size int64) (System, error) {
	fs, err := installeriso.NewForInspection(r, size, nopCloser{})
	if err != nil {
		return System{}, err
	}
	defer fs.Close()
	return identify(newISOSource(fs)), nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// source is the view of an image the detectors need. Paths are lower case,
// slash-separated and relative to the image root.
type source interface {
	label() string
	has(path string) bool
	hasDir(path string) bool
	readFile(path string, limit uint64) ([]byte, error)
	open(path string) (io.ReaderAt, int64, error)
}

type isoSource struct {
	fs      *installeriso.Reader
	entries map[string]installeriso.Entry
}

func newISOSource(fs *installeriso.Reader) isoSource {
	entries := map[string]installeriso.Entry{}
	for _, e := range fs.Manifest().Entries {
		entries[strings.ToLower(e.Path)] = e
	}
	return isoSource{fs: fs, entries: entries}
}

func (s isoSource) label() string { return s.fs.VolumeLabel() }

func (s isoSource) has(path string) bool {
	e, ok := s.entries[path]
	return ok && e.Type == installeriso.File
}

func (s isoSource) hasDir(path string) bool {
	e, ok := s.entries[path]
	return ok && e.Type == installeriso.Directory
}

func (s isoSource) readFile(path string, limit uint64) ([]byte, error) {
	e, ok := s.entries[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return s.fs.ReadFile(e, limit)
}

func (s isoSource) open(path string) (io.ReaderAt, int64, error) {
	e, ok := s.entries[path]
	if !ok {
		return nil, 0, os.ErrNotExist
	}
	f, err := s.fs.FileReader(e)
	if err != nil {
		return nil, 0, err
	}
	return f, f.Size(), nil
}

// identify runs the detectors from the most to the least specific evidence.
func identify(src source) System {
	label := clean(src.label())
	for _, detect := range []func(source, string) (System, bool){detectWindows, detectLinux} {
		if s, ok := detect(src, label); ok {
			s.Label = label
			return s
		}
	}
	return System{Label: label}
}

// maxText bounds every string taken from the image.
const maxText = 128

// clean makes image text safe to display: control characters removed, runs
// of white space collapsed, and the length bounded.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == unicode.ReplacementChar:
			return -1
		case unicode.IsSpace(r):
			return ' '
		case !unicode.IsPrint(r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxText {
		s = string(r[:maxText])
	}
	return s
}

// normArch maps the many spellings of an architecture to one.
func normArch(s string) string {
	switch strings.ToLower(s) {
	case "x86_64", "amd64", "x64", "x86-64":
		return "x86_64"
	case "aarch64", "arm64", "a64":
		return "arm64"
	case "i386", "i486", "i586", "i686", "x86":
		return "x86"
	case "ppc64le", "s390x", "riscv64", "armhf", "armv7hl":
		return strings.ToLower(s)
	}
	return ""
}
