package osinfo

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

var windowsInstallImages = []string{"sources/install.wim", "sources/install.esd", "sources/install.swm"}

func detectWindows(src source, label string) (System, bool) {
	if !src.has("sources/boot.wim") && !src.has("bootmgr") && !hasAnyFile(src, windowsInstallImages) {
		return System{}, false
	}
	s := System{Family: Windows, Name: "Windows"}
	for _, p := range windowsInstallImages {
		if !src.has(p) {
			continue
		}
		if r, size, err := src.open(p); err == nil {
			if images, err := readWIMImages(r, size); err == nil && len(images) != 0 {
				applyWIMImages(&s, images)
				return s, true
			}
		}
	}
	applyWindowsLabel(&s, label)
	return s, true
}

func hasAnyFile(src source, paths []string) bool {
	for _, p := range paths {
		if src.has(p) {
			return true
		}
	}
	return false
}

// WIM header layout (WIMHEADER_V1_PACKED): the magic, the header size, and at
// offset 72 the resource header of the XML description.
const (
	wimHeaderSize    = 208
	wimXMLReshdr     = 72
	reshdrCompressed = 0x04
	reshdrSolid      = 0x10
	// maxWIMXML bounds the XML read; real media carry tens of kilobytes.
	maxWIMXML = 4 << 20
)

var (
	wimMagic       = []byte("MSWIM\x00\x00\x00")
	errNotWIM      = errors.New("not a WIM file")
	errWIMXMLRange = errors.New("WIM XML description is missing or out of range")
)

// readWIMImages reads the per-image descriptions from a WIM or ESD file's XML
// resource, which is stored uncompressed even when the payload is not.
func readWIMImages(r io.ReaderAt, size int64) ([]wimImage, error) {
	h := make([]byte, wimHeaderSize)
	if _, err := r.ReadAt(h, 0); err != nil {
		return nil, err
	}
	if !bytes.Equal(h[:8], wimMagic) || binary.LittleEndian.Uint32(h[8:12]) != wimHeaderSize {
		return nil, errNotWIM
	}
	res := h[wimXMLReshdr : wimXMLReshdr+24]
	var sizeBytes [8]byte
	copy(sizeBytes[:7], res[:7])
	length := binary.LittleEndian.Uint64(sizeBytes[:])
	flags := res[7]
	offset := binary.LittleEndian.Uint64(res[8:16])
	if flags&(reshdrCompressed|reshdrSolid) != 0 || length == 0 || length > maxWIMXML ||
		offset > uint64(size) || length > uint64(size)-offset {
		return nil, errWIMXMLRange
	}
	data := make([]byte, length)
	if _, err := r.ReadAt(data, int64(offset)); err != nil {
		return nil, err
	}
	return parseWIMXML(data)
}

type wimImage struct {
	Name        string `xml:"NAME"`
	DisplayName string `xml:"DISPLAYNAME"`
	Windows     struct {
		Arch             string `xml:"ARCH"`
		InstallationType string `xml:"INSTALLATIONTYPE"`
		Languages        struct {
			Default string `xml:"DEFAULT"`
		} `xml:"LANGUAGES"`
		Version struct {
			Major   string `xml:"MAJOR"`
			Minor   string `xml:"MINOR"`
			Build   string `xml:"BUILD"`
			SPBuild string `xml:"SPBUILD"`
		} `xml:"VERSION"`
	} `xml:"WINDOWS"`
}

// maxWIMImages bounds the editions kept from one image.
const maxWIMImages = 64

// parseWIMXML decodes the UTF-16LE XML description of a WIM.
func parseWIMXML(data []byte) ([]wimImage, error) {
	data = bytes.TrimPrefix(data, []byte{0xff, 0xfe})
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(data[2*i:])
	}
	var doc struct {
		Images []wimImage `xml:"IMAGE"`
	}
	dec := xml.NewDecoder(strings.NewReader(string(utf16.Decode(units))))
	// The text is already UTF-8; a declaration naming UTF-16 must not stop it.
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if len(doc.Images) > maxWIMImages {
		doc.Images = doc.Images[:maxWIMImages]
	}
	return doc.Images, nil
}

func applyWIMImages(s *System, images []wimImage) {
	first := images[0].Windows
	major, minor, build := atoi(first.Version.Major), atoi(first.Version.Minor), atoi(first.Version.Build)
	server := strings.Contains(first.InstallationType, "Server")
	s.Name, s.Version = windowsRelease(major, minor, build, server)
	if build > 0 {
		s.Build = strconv.Itoa(build)
		if sp := atoi(first.Version.SPBuild); sp > 0 {
			s.Build += "." + strconv.Itoa(sp)
		}
	}
	s.Arch = wimArch(first.Arch)
	s.Language = clean(first.Languages.Default)
	seen := map[string]bool{}
	for _, img := range images {
		name := clean(img.Name)
		if name == "" {
			name = clean(img.DisplayName)
		}
		if name != "" && !seen[name] {
			seen[name] = true
			s.Editions = append(s.Editions, name)
		}
	}
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// wimArch maps the PROCESSOR_ARCHITECTURE value WIM XML records.
func wimArch(s string) string {
	switch strings.TrimSpace(s) {
	case "0":
		return "x86"
	case "5":
		return "arm"
	case "9":
		return "x86_64"
	case "12":
		return "arm64"
	}
	return ""
}

// windowsRelease names the release a Windows version number belongs to.
func windowsRelease(major, minor, build int, server bool) (name, version string) {
	if server {
		switch {
		case build >= 26100:
			return "Windows Server 2025", ""
		case build >= 20348:
			return "Windows Server 2022", ""
		case build >= 17763:
			return "Windows Server 2019", ""
		case build >= 14393:
			return "Windows Server 2016", ""
		}
		return "Windows Server", ""
	}
	switch {
	case major == 10 && build >= 22000:
		return "Windows 11", featureUpdate(build)
	case major == 10:
		return "Windows 10", featureUpdate(build)
	case major == 6 && minor == 3:
		return "Windows 8.1", ""
	case major == 6 && minor == 2:
		return "Windows 8", ""
	case major == 6 && minor == 1:
		return "Windows 7", ""
	}
	return "Windows", ""
}

// featureUpdate names the feature update a Windows 10 or 11 build shipped as.
// Builds missing from the table are reported by number only.
func featureUpdate(build int) string {
	return map[int]string{
		19041: "2004", 19042: "20H2", 19043: "21H1", 19044: "21H2", 19045: "22H2",
		22000: "21H2", 22621: "22H2", 22631: "23H2", 26100: "24H2", 26200: "25H2",
	}[build]
}

// applyWindowsLabel reads what a Microsoft volume label such as
// CCCOMA_X64FRE_ZH-TW_DV9 says about architecture and language.
func applyWindowsLabel(s *System, label string) {
	parts := strings.Split(strings.ToUpper(label), "_")
	for i, p := range parts {
		if !strings.HasSuffix(p, "FRE") && !strings.HasSuffix(p, "CHK") {
			continue
		}
		s.Arch = normArch(strings.TrimSuffix(strings.TrimSuffix(p, "FRE"), "CHK"))
		if i+1 < len(parts) && len(parts[i+1]) == 5 && parts[i+1][2] == '-' {
			s.Language = strings.ToLower(parts[i+1][:2]) + "-" + parts[i+1][3:]
		}
		return
	}
}
