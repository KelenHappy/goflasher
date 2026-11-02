package osinfo

import (
	"bufio"
	"bytes"
	"strings"
)

// maxInfoFile bounds the small text files the Linux detectors read.
const maxInfoFile = 64 << 10

// linuxMarkers are files and directories that only Linux live and installer
// media carry, used when no release file names the distribution.
var (
	linuxMarkerDirs  = []string{"casper", "live", "liveos", "isolinux", "boot/grub", "boot/grub2", "arch", "images/pxeboot", "syslinux"}
	linuxMarkerFiles = []string{"efi/boot/grubx64.efi", "efi/boot/grubaa64.efi", "boot/grub/grub.cfg", "efi/boot/grub.cfg"}
)

func detectLinux(src source, label string) (System, bool) {
	for _, detect := range []func(source, string) (System, bool){fromTreeinfo, fromDiskInfo, fromDiscinfo, fromArchLabel, fromFedoraLabel, fromDistroLabel} {
		if s, ok := detect(src, label); ok {
			s.Family = Linux
			return s, true
		}
	}
	if !hasLinuxMarkers(src) {
		return System{}, false
	}
	s := System{Family: Linux, Name: label}
	if s.Name == "" {
		s.Name = "Linux"
	}
	s.Arch = archFromTokens(tokens(label))
	return s, true
}

func hasLinuxMarkers(src source) bool {
	for _, d := range linuxMarkerDirs {
		if src.hasDir(d) {
			return true
		}
	}
	return hasAnyFile(src, linuxMarkerFiles)
}

// fromTreeinfo reads the productmd .treeinfo that Fedora, RHEL, CentOS,
// Rocky and AlmaLinux installer media carry.
func fromTreeinfo(src source, _ string) (System, bool) {
	data, err := src.readFile(".treeinfo", maxInfoFile)
	if err != nil {
		return System{}, false
	}
	ini := parseINI(data)
	name := first(ini["release.name"], ini["general.family"], ini["product.name"])
	if name == "" {
		return System{}, false
	}
	if variant := first(ini["general.variant"], ini["variant.name"]); variant != "" && variant != "Everything" && !strings.Contains(name, variant) {
		name += " " + variant
	}
	return System{
		Name:    name,
		Version: first(ini["release.version"], ini["general.version"], ini["product.version"]),
		Arch:    normArch(first(ini["tree.arch"], ini["general.arch"])),
	}, true
}

// parseINI flattens an INI file to "section.key" keys with cleaned values.
func parseINI(data []byte) map[string]string {
	out := map[string]string{}
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
		case line[0] == '[' && line[len(line)-1] == ']':
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
		default:
			key, value, ok := strings.Cut(line, "=")
			if ok {
				out[section+"."+strings.ToLower(strings.TrimSpace(key))] = clean(value)
			}
		}
	}
	return out
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// fromDiskInfo reads .disk/info, the one-line description Debian, Ubuntu and
// their derivatives ship, such as
// Ubuntu 24.04.1 LTS "Noble Numbat" - Release amd64 (20240827).
func fromDiskInfo(src source, _ string) (System, bool) {
	data, err := src.readFile(".disk/info", maxInfoFile)
	if err != nil {
		return System{}, false
	}
	line := clean(string(firstLine(data)))
	if line == "" {
		return System{}, false
	}
	head := line
	if i := strings.IndexAny(head, "\"("); i >= 0 {
		head = head[:i]
	}
	head, _, _ = strings.Cut(head, " - ")
	name, version := splitVersion(strings.TrimSpace(head))
	return System{Name: name, Version: version, Arch: archFromTokens(tokens(line))}, name != ""
}

// splitVersion splits "Ubuntu 24.04.1 LTS" at its first word that starts with
// a digit into the name and the version.
func splitVersion(s string) (name, version string) {
	words := strings.Fields(s)
	for i, w := range words {
		if i > 0 && w[0] >= '0' && w[0] <= '9' {
			return strings.Join(words[:i], " "), strings.Join(words[i:], " ")
		}
	}
	return s, ""
}

// fromDiscinfo reads .discinfo: a timestamp, the release ("Fedora 40" or just
// "40"), and the architecture, one per line.
func fromDiscinfo(src source, _ string) (System, bool) {
	data, err := src.readFile(".discinfo", maxInfoFile)
	if err != nil {
		return System{}, false
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) < 3 {
		return System{}, false
	}
	name, version := splitVersion(clean(lines[1]))
	if name != "" && name[0] >= '0' && name[0] <= '9' {
		name, version = "", name
	}
	if name == "" {
		return System{}, false
	}
	return System{Name: name, Version: version, Arch: normArch(clean(lines[2]))}, true
}

// fromArchLabel recognizes Arch Linux media, labelled ARCH_YYYYMM.
func fromArchLabel(src source, label string) (System, bool) {
	date, ok := strings.CutPrefix(label, "ARCH_")
	if !ok || !src.hasDir("arch") {
		return System{}, false
	}
	s := System{Name: "Arch Linux", Arch: "x86_64"}
	if len(date) == 6 && isDigits(date) {
		s.Version = date[:4] + "." + date[4:]
	}
	return s, true
}

// fedoraVariants expands the variant abbreviations in Fedora volume labels.
var fedoraVariants = map[string]string{
	"ws": "Workstation", "s": "Server", "e": "Everything", "kde": "KDE Plasma Desktop",
	"iot": "IoT", "budgie": "Budgie", "cinnamon": "Cinnamon", "xfce": "Xfce", "lxqt": "LXQt",
	"mate": "MATE", "sway": "Sway", "i3": "i3", "silverblue": "Silverblue", "kinoite": "Kinoite",
}

// fromFedoraLabel recognizes Fedora live media, whose only identification is
// a label such as Fedora-WS-Live-44-1-5.
func fromFedoraLabel(_ source, label string) (System, bool) {
	words := tokens(label)
	if len(words) == 0 || !strings.EqualFold(words[0], "fedora") {
		return System{}, false
	}
	s := System{Name: "Fedora", Arch: archFromTokens(words)}
	for _, w := range words[1:] {
		switch {
		case s.Version == "" && len(w) == 2 && isDigits(w):
			s.Version = w
		case fedoraVariants[strings.ToLower(w)] != "" && s.Name == "Fedora":
			s.Name += " " + fedoraVariants[strings.ToLower(w)]
		}
	}
	return s, true
}

func tokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '-' || r == ' ' || r == '(' || r == ')' || r == ','
	})
}

func archFromTokens(words []string) string {
	for _, w := range words {
		if a := normArch(w); a != "" {
			return a
		}
	}
	return ""
}

func firstLine(b []byte) []byte {
	line, _, _ := bytes.Cut(b, []byte("\n"))
	return line
}

func isDigits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// distroLabels names the distributions whose live media are identified only
// by a volume label such as Rocky-10-2-KDE, keyed by the label's first word.
var distroLabels = map[string]string{
	"rocky": "Rocky Linux", "almalinux": "AlmaLinux", "alma": "AlmaLinux", "centos": "CentOS",
	"opensuse": "openSUSE", "ubuntu": "Ubuntu", "kubuntu": "Kubuntu", "xubuntu": "Xubuntu",
	"debian": "Debian", "linuxmint": "Linux Mint", "mint": "Linux Mint", "kali": "Kali Linux",
	"manjaro": "Manjaro", "nixos": "NixOS", "pop_os": "Pop!_OS", "endeavouros": "EndeavourOS",
	"zorin": "Zorin OS", "elementary": "elementary OS", "rhel": "Red Hat Enterprise Linux",
}

// labelNoise are label words that say nothing about the system itself.
var labelNoise = map[string]bool{"live": true, "dvd": true, "dvd1": true, "boot": true, "netinst": true, "iso": true, "cd": true, "media": true}

// fromDistroLabel reads a known distribution's volume label: its first word
// names the distribution, numeric words form the version, and the remaining
// words name the variant. Only media with Linux boot markers qualify.
func fromDistroLabel(src source, label string) (System, bool) {
	words := tokens(label)
	if len(words) == 0 || !hasLinuxMarkers(src) {
		return System{}, false
	}
	name, ok := distroLabels[strings.ToLower(words[0])]
	if !ok {
		return System{}, false
	}
	s := System{Name: name, Arch: archFromTokens(words)}
	var version, variant []string
	for _, w := range words[1:] {
		lower := strings.ToLower(w)
		switch {
		case normArch(w) != "" || labelNoise[lower]:
		case isVersion(w) && len(variant) == 0:
			version = append(version, w)
		case lower == "lts" && len(version) != 0:
			version = append(version, "LTS")
		case len(variant) < 2 && !isVersion(w):
			variant = append(variant, w)
		}
	}
	if len(variant) != 0 {
		s.Name += " " + strings.Join(variant, " ")
	}
	s.Version = joinVersion(version)
	return s, true
}

// isVersion reports a word made of digits and dots, such as 10 or 24.04.1.
func isVersion(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9' && strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' }) < 0
}

// joinVersion joins numeric words with dots (Rocky-10-2 is 10.2) and keeps a
// trailing LTS apart.
func joinVersion(words []string) string {
	var nums []string
	suffix := ""
	for _, w := range words {
		if w == "LTS" {
			suffix = " LTS"
			continue
		}
		nums = append(nums, w)
	}
	if len(nums) == 0 {
		return ""
	}
	return strings.Join(nums, ".") + suffix
}
