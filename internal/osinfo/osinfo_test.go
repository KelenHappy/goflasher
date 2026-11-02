package osinfo

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"slices"
	"testing"
	"unicode/utf16"
)

// fakeSource is an in-memory image: files by path, directories by path.
type fakeSource struct {
	volume string
	files  map[string][]byte
	dirs   []string
}

func (f fakeSource) label() string           { return f.volume }
func (f fakeSource) has(path string) bool    { _, ok := f.files[path]; return ok }
func (f fakeSource) hasDir(path string) bool { return slices.Contains(f.dirs, path) }
func (f fakeSource) readFile(path string, limit uint64) ([]byte, error) {
	b, ok := f.files[path]
	if !ok || uint64(len(b)) > limit {
		return nil, os.ErrNotExist
	}
	return b, nil
}
func (f fakeSource) open(path string) (io.ReaderAt, int64, error) {
	b, ok := f.files[path]
	if !ok {
		return nil, 0, os.ErrNotExist
	}
	return bytes.NewReader(b), int64(len(b)), nil
}

// testWIM builds a WIM header followed by a UTF-16LE XML description.
func testWIM(xml string, flags byte) []byte {
	units := utf16.Encode([]rune(xml))
	data := []byte{0xff, 0xfe}
	for _, u := range units {
		data = binary.LittleEndian.AppendUint16(data, u)
	}
	h := make([]byte, wimHeaderSize)
	copy(h, wimMagic)
	binary.LittleEndian.PutUint32(h[8:], wimHeaderSize)
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], uint64(len(data)))
	copy(h[wimXMLReshdr:], size[:7])
	h[wimXMLReshdr+7] = flags
	binary.LittleEndian.PutUint64(h[wimXMLReshdr+8:], wimHeaderSize+100)
	binary.LittleEndian.PutUint64(h[wimXMLReshdr+16:], uint64(len(data)))
	out := append(h, make([]byte, 100)...)
	return append(out, data...)
}

const win11XML = `<WIM><TOTALBYTES>1</TOTALBYTES>` +
	`<IMAGE INDEX="1"><NAME>Windows 11 Home</NAME><WINDOWS><ARCH>9</ARCH><INSTALLATIONTYPE>Client</INSTALLATIONTYPE>` +
	`<LANGUAGES><LANGUAGE>zh-TW</LANGUAGE><DEFAULT>zh-TW</DEFAULT></LANGUAGES>` +
	`<VERSION><MAJOR>10</MAJOR><MINOR>0</MINOR><BUILD>26100</BUILD><SPBUILD>1742</SPBUILD></VERSION></WINDOWS></IMAGE>` +
	`<IMAGE INDEX="2"><NAME>Windows 11 Pro</NAME><WINDOWS><ARCH>9</ARCH></WINDOWS></IMAGE>` +
	`<IMAGE INDEX="3"><NAME>Windows 11 Pro</NAME></IMAGE></WIM>`

func TestIdentifyWindowsFromWIMDescription(t *testing.T) {
	src := fakeSource{volume: "CCCOMA_X64FRE_ZH-TW_DV9", files: map[string][]byte{
		"bootmgr": {}, "sources/boot.wim": {}, "sources/install.wim": testWIM(win11XML, 0),
	}}
	got := identify(src)
	want := System{Family: Windows, Name: "Windows 11", Version: "24H2", Build: "26100.1742", Arch: "x86_64",
		Language: "zh-TW", Editions: []string{"Windows 11 Home", "Windows 11 Pro"}, Label: "CCCOMA_X64FRE_ZH-TW_DV9"}
	if !equalSystem(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if s := got.Summary(); s != "Windows 11 24H2 (build 26100.1742, x86_64, zh-TW)" {
		t.Fatalf("summary=%q", s)
	}
}

func TestIdentifyWindowsESDWithDeclaration(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-16"?><WIM><IMAGE INDEX="1"><NAME>Windows 10 Pro</NAME>` +
		`<WINDOWS><ARCH>0</ARCH><VERSION><MAJOR>10</MAJOR><MINOR>0</MINOR><BUILD>19045</BUILD></VERSION></WINDOWS></IMAGE></WIM>`
	got := identify(fakeSource{files: map[string][]byte{"sources/install.esd": testWIM(xml, 0)}})
	if got.Name != "Windows 10" || got.Version != "22H2" || got.Build != "19045" || got.Arch != "x86" {
		t.Fatalf("got %+v", got)
	}
}

func TestIdentifyWindowsFallsBackToLabel(t *testing.T) {
	for name, wim := range map[string][]byte{
		"compressed xml": testWIM(win11XML, reshdrCompressed),
		"bad magic":      append([]byte("NOTAWIM!"), testWIM(win11XML, 0)[8:]...),
		"truncated":      testWIM(win11XML, 0)[:300],
		"empty":          {},
	} {
		got := identify(fakeSource{volume: "CCCOMA_A64FRE_EN-US_DV9", files: map[string][]byte{"sources/install.wim": wim}})
		if got.Family != Windows || got.Name != "Windows" || got.Arch != "arm64" || got.Language != "en-US" || got.Build != "" {
			t.Errorf("%s: got %+v", name, got)
		}
	}
}

func TestWindowsRelease(t *testing.T) {
	for _, tc := range []struct {
		major, minor, build int
		server              bool
		name, version       string
	}{
		{10, 0, 22631, false, "Windows 11", "23H2"},
		{10, 0, 27000, false, "Windows 11", ""},
		{10, 0, 19044, false, "Windows 10", "21H2"},
		{6, 3, 9600, false, "Windows 8.1", ""},
		{6, 1, 7601, false, "Windows 7", ""},
		{10, 0, 20348, true, "Windows Server 2022", ""},
		{10, 0, 26100, true, "Windows Server 2025", ""},
	} {
		name, version := windowsRelease(tc.major, tc.minor, tc.build, tc.server)
		if name != tc.name || version != tc.version {
			t.Errorf("%+v: got %q %q", tc, name, version)
		}
	}
}

func TestIdentifyLinux(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  fakeSource
		want System
	}{
		{"fedora live label", fakeSource{volume: "Fedora-WS-Live-44-1-5", dirs: []string{"liveos"}},
			System{Family: Linux, Name: "Fedora Workstation", Version: "44", Label: "Fedora-WS-Live-44-1-5"}},
		{"fedora kde aarch64 label", fakeSource{volume: "Fedora-KDE-Live-aarch64-43", dirs: []string{"liveos"}},
			System{Family: Linux, Name: "Fedora KDE Plasma Desktop", Version: "43", Arch: "arm64", Label: "Fedora-KDE-Live-aarch64-43"}},
		{"fedora treeinfo", fakeSource{volume: "Fedora-S-dvd-x86_64-44", files: map[string][]byte{".treeinfo": []byte(
			"[header]\ntype = productmd.treeinfo\n\n[release]\nname = Fedora\nshort = Fedora\nversion = 44\n\n[general]\nvariant = Server\n\n[tree]\narch = x86_64\n")}},
			System{Family: Linux, Name: "Fedora Server", Version: "44", Arch: "x86_64", Label: "Fedora-S-dvd-x86_64-44"}},
		{"rocky treeinfo", fakeSource{files: map[string][]byte{".treeinfo": []byte(
			"[release]\nname = Rocky Linux\nversion = 9.5\n[tree]\narch = aarch64\n")}},
			System{Family: Linux, Name: "Rocky Linux", Version: "9.5", Arch: "arm64"}},
		{"ubuntu disk info", fakeSource{volume: "Ubuntu 24.04.1 LTS amd64", files: map[string][]byte{".disk/info": []byte(
			`Ubuntu 24.04.1 LTS "Noble Numbat" - Release amd64 (20240827)`)}},
			System{Family: Linux, Name: "Ubuntu", Version: "24.04.1 LTS", Arch: "x86_64", Label: "Ubuntu 24.04.1 LTS amd64"}},
		{"debian disk info", fakeSource{files: map[string][]byte{".disk/info": []byte(
			"Debian GNU/Linux 12.7.0 \"Bookworm\" - Official amd64 NETINST with firmware 20240831-10:40\n")}},
			System{Family: Linux, Name: "Debian GNU/Linux", Version: "12.7.0", Arch: "x86_64"}},
		{"mint disk info", fakeSource{files: map[string][]byte{".disk/info": []byte(`Linux Mint 22 "Wilma" - Release amd64 20240722`)}},
			System{Family: Linux, Name: "Linux Mint", Version: "22", Arch: "x86_64"}},
		{"discinfo", fakeSource{files: map[string][]byte{".discinfo": []byte("1715000000.0\nCentOS Stream 9\nx86_64\n")}},
			System{Family: Linux, Name: "CentOS Stream", Version: "9", Arch: "x86_64"}},
		{"arch label", fakeSource{volume: "ARCH_202611", dirs: []string{"arch"}},
			System{Family: Linux, Name: "Arch Linux", Version: "2026.11", Arch: "x86_64", Label: "ARCH_202611"}},
		{"rocky live label", fakeSource{volume: "Rocky-10-2-KDE", dirs: []string{"liveos"}},
			System{Family: Linux, Name: "Rocky Linux KDE", Version: "10.2", Label: "Rocky-10-2-KDE"}},
		{"ubuntu label without disk info", fakeSource{volume: "Ubuntu 24.04.1 LTS amd64", dirs: []string{"casper"}},
			System{Family: Linux, Name: "Ubuntu", Version: "24.04.1 LTS", Arch: "x86_64", Label: "Ubuntu 24.04.1 LTS amd64"}},
		{"distro label needs boot markers", fakeSource{volume: "Rocky-10-2-KDE"},
			System{Label: "Rocky-10-2-KDE"}},
		{"opensuse label", fakeSource{volume: "openSUSE-Tumbleweed-DVD-x86_64", dirs: []string{"boot/grub2"}},
			System{Family: Linux, Name: "openSUSE Tumbleweed", Arch: "x86_64", Label: "openSUSE-Tumbleweed-DVD-x86_64"}},
		{"unknown distribution", fakeSource{volume: "MYDISTRO-7-aarch64", dirs: []string{"boot/grub"}},
			System{Family: Linux, Name: "MYDISTRO-7-aarch64", Arch: "arm64", Label: "MYDISTRO-7-aarch64"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := identify(tc.src); !equalSystem(got, tc.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestIdentifyUnknownKeepsOnlyCleanLabel(t *testing.T) {
	got := identify(fakeSource{volume: "MY\x00 DISC\x1b[31m  \t", files: map[string][]byte{"readme.txt": {}}})
	if got.Known() || got.Label != "MY DISC[31m" {
		t.Fatalf("got %+v", got)
	}
}

func TestSummaryFallsBackToLabel(t *testing.T) {
	if got := (System{Label: "DATA"}).Summary(); got != "DATA" {
		t.Fatalf("summary=%q", got)
	}
}

func equalSystem(a, b System) bool {
	return a.Family == b.Family && a.Name == b.Name && a.Version == b.Version && a.Build == b.Build &&
		a.Arch == b.Arch && a.Language == b.Language && a.Label == b.Label && slices.Equal(a.Editions, b.Editions)
}

func FuzzIdentify(f *testing.F) {
	f.Add(testWIM(win11XML, 0), []byte("[release]\nname = Fedora\nversion = 44\n"), "Fedora-WS-Live-44-1-5")
	f.Add([]byte{}, []byte(`Ubuntu 24.04 LTS "Noble" - Release amd64`), "CCCOMA_X64FRE_EN-US_DV9")
	f.Fuzz(func(t *testing.T, wim, text []byte, label string) {
		for _, src := range []fakeSource{
			{volume: label, files: map[string][]byte{"sources/install.wim": wim}},
			{volume: label, files: map[string][]byte{".treeinfo": text, ".disk/info": text, ".discinfo": text}},
			{volume: label, dirs: []string{"arch", "liveos"}},
		} {
			s := identify(src)
			for _, v := range append([]string{s.Name, s.Version, s.Build, s.Arch, s.Language, s.Label}, s.Editions...) {
				if len([]rune(v)) > maxText+32 {
					t.Fatalf("unbounded text %q", v)
				}
			}
			_ = s.Summary()
		}
	})
}
