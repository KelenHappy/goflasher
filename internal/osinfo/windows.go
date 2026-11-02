package osinfo

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

var (
	windowsInstallImages = []imagePath{"sources/install.wim", "sources/install.esd", "sources/install.swm"}
	// windowsSignals are files only Windows installer media carry.
	windowsSignals = append([]imagePath{"sources/boot.wim", "bootmgr"}, windowsInstallImages...)
)

func detectWindows(src source, label volumeLabel) (System, bool) {
	if !hasAnyFile(src, windowsSignals) {
		return System{}, false
	}
	s := System{Family: Windows, Name: "Windows"}
	if images := installImages(src); len(images) != 0 {
		applyWIMImages(&s, images)
	} else {
		applyWindowsLabel(&s, label)
	}
	return s, true
}

func hasAnyFile(src source, paths []imagePath) bool {
	return slices.ContainsFunc(paths, src.has)
}

// installImages returns the image descriptions of the first install image
// whose XML description can be read, or nil.
func installImages(src source) []wimImage {
	for _, p := range windowsInstallImages {
		if images := wimImagesAt(src, p); len(images) != 0 {
			return images
		}
	}
	return nil
}

func wimImagesAt(src source, path imagePath) []wimImage {
	if !src.has(path) {
		return nil
	}
	r, size, err := src.open(path)
	if err != nil {
		return nil
	}
	images, err := readWIMImages(r, size)
	if err != nil {
		return nil
	}
	return images
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
	if !validWIMHeader(h) {
		return nil, errNotWIM
	}
	xmlRes := parseResourceHeader(h[wimXMLReshdr : wimXMLReshdr+24])
	if !xmlRes.readableXML(uint64(size)) {
		return nil, errWIMXMLRange
	}
	data := make([]byte, xmlRes.length)
	if _, err := r.ReadAt(data, int64(xmlRes.offset)); err != nil {
		return nil, err
	}
	return parseWIMXML(data)
}

func validWIMHeader(h []byte) bool {
	return bytes.Equal(h[:8], wimMagic) && binary.LittleEndian.Uint32(h[8:12]) == wimHeaderSize
}

// resourceHeader is a WIM RESHDR_DISK_SHORT: a 56-bit stored size, a flags
// byte, and the resource's offset in the file.
type resourceHeader struct {
	length, offset uint64
	flags          byte
}

func parseResourceHeader(b []byte) resourceHeader {
	var length [8]byte
	copy(length[:7], b[:7])
	return resourceHeader{length: binary.LittleEndian.Uint64(length[:]), flags: b[7], offset: binary.LittleEndian.Uint64(b[8:16])}
}

// packed reports a compressed or solid resource, which this reader cannot
// decode.
func (h resourceHeader) packed() bool { return h.flags&(reshdrCompressed|reshdrSolid) != 0 }

// within reports whether the resource lies inside a file of size bytes.
func (h resourceHeader) within(size uint64) bool {
	return h.offset <= size && h.length <= size-h.offset
}

// readableXML reports an uncompressed, bounded XML description inside the
// file.
func (h resourceHeader) readableXML(size uint64) bool {
	if h.packed() || !h.within(size) {
		return false
	}
	return h.length > 0 && h.length <= maxWIMXML
}

type wimImage struct {
	Name        string     `xml:"NAME"`
	DisplayName string     `xml:"DISPLAYNAME"`
	Windows     wimWindows `xml:"WINDOWS"`
}

type wimWindows struct {
	Arch             string `xml:"ARCH"`
	InstallationType string `xml:"INSTALLATIONTYPE"`
	Languages        struct {
		Default string `xml:"DEFAULT"`
	} `xml:"LANGUAGES"`
	Version wimVersion `xml:"VERSION"`
}

type wimVersion struct {
	Major   string `xml:"MAJOR"`
	Minor   string `xml:"MINOR"`
	Build   string `xml:"BUILD"`
	SPBuild string `xml:"SPBUILD"`
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

// applyWIMImages describes the release from the first image, which every
// edition on the media shares, and lists the editions.
func applyWIMImages(s *System, images []wimImage) {
	w := images[0].Windows
	v := w.Version
	s.Name, s.Version = windowsRelease(atoi(v.Major), atoi(v.Minor), atoi(v.Build), strings.Contains(w.InstallationType, "Server"))
	s.Build = v.build()
	s.Arch = wimArch(w.Arch)
	s.Language = clean(w.Languages.Default)
	s.Editions = editionNames(images)
}

// build is the build number with its servicing revision, such as
// 26100.1742, or "" when the image records none.
func (v wimVersion) build() string {
	build := atoi(v.Build)
	if build == 0 {
		return ""
	}
	text := strconv.Itoa(build)
	if sp := atoi(v.SPBuild); sp > 0 {
		text += "." + strconv.Itoa(sp)
	}
	return text
}

// editionNames lists each image's name once, in image order.
func editionNames(images []wimImage) []string {
	var names []string
	for _, img := range images {
		if name := img.editionName(); name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

func (img wimImage) editionName() string {
	if name := clean(img.Name); name != "" {
		return name
	}
	return clean(img.DisplayName)
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
	return map[string]string{"0": "x86", "5": "arm", "9": "x86_64", "12": "arm64"}[strings.TrimSpace(s)]
}

// serverReleases are the Windows Server releases by first build, newest first.
var serverReleases = []struct {
	build int
	name  string
}{
	{26100, "Windows Server 2025"}, {20348, "Windows Server 2022"},
	{17763, "Windows Server 2019"}, {14393, "Windows Server 2016"},
}

// legacyClients are the client releases before Windows 10, by major.minor.
var legacyClients = map[[2]int]string{{6, 3}: "Windows 8.1", {6, 2}: "Windows 8", {6, 1}: "Windows 7"}

// featureUpdates name the feature update a Windows 10 or 11 build shipped
// as. Builds missing from the table are reported by number only.
var featureUpdates = map[int]string{
	19041: "2004", 19042: "20H2", 19043: "21H1", 19044: "21H2", 19045: "22H2",
	22000: "21H2", 22621: "22H2", 22631: "23H2", 26100: "24H2", 26200: "25H2",
}

// windowsRelease names the release a Windows version number belongs to.
func windowsRelease(major, minor, build int, server bool) (name, version string) {
	if server {
		return serverRelease(build), ""
	}
	if major == 10 {
		return clientRelease(build), featureUpdates[build]
	}
	if name, ok := legacyClients[[2]int{major, minor}]; ok {
		return name, ""
	}
	return "Windows", ""
}

func serverRelease(build int) string {
	for _, r := range serverReleases {
		if build >= r.build {
			return r.name
		}
	}
	return "Windows Server"
}

// clientRelease tells Windows 11 from Windows 10, which share version 10.0.
func clientRelease(build int) string {
	if build >= 22000 {
		return "Windows 11"
	}
	return "Windows 10"
}

// applyWindowsLabel reads what a Microsoft volume label such as
// CCCOMA_X64FRE_ZH-TW_DV9 says about architecture and language: the part
// ending in FRE or CHK names the architecture, and the next part the language.
func applyWindowsLabel(s *System, label volumeLabel) {
	parts := strings.Split(strings.ToUpper(string(label)), "_")
	i := slices.IndexFunc(parts, isBuildFlavor)
	if i < 0 {
		return
	}
	s.Arch = normArch(parts[i][:len(parts[i])-3])
	if i+1 < len(parts) {
		s.Language = labelLanguage(parts[i+1])
	}
}

// isBuildFlavor reports a label part such as X64FRE: an architecture followed
// by a free or checked build marker.
func isBuildFlavor(part string) bool {
	return strings.HasSuffix(part, "FRE") || strings.HasSuffix(part, "CHK")
}

// labelLanguage turns a label part such as ZH-TW into zh-TW, and anything that
// is not a language-region pair into "".
func labelLanguage(part string) string {
	if len(part) != 5 || part[2] != '-' {
		return ""
	}
	return strings.ToLower(part[:2]) + "-" + part[3:]
}
