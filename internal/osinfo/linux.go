package osinfo

import (
	"bufio"
	"bytes"
	"slices"
	"strings"
)

// maxInfoFile bounds the small text files the Linux detectors read.
const maxInfoFile = 64 << 10

// linuxMarkers are files and directories that only Linux live and installer
// media carry, used when no release file names the distribution.
var (
	linuxMarkerDirs  = []imagePath{"casper", "live", "liveos", "isolinux", "boot/grub", "boot/grub2", "arch", "images/pxeboot", "syslinux"}
	linuxMarkerFiles = []imagePath{"efi/boot/grubx64.efi", "efi/boot/grubaa64.efi", "boot/grub/grub.cfg", "efi/boot/grub.cfg"}
)

func detectLinux(src source, label volumeLabel) (System, bool) {
	for _, detect := range []func(source, volumeLabel) (System, bool){fromTreeinfo, fromDiskInfo, fromDiscinfo, fromArchLabel, fromFedoraLabel, fromDistroLabel} {
		if s, ok := detect(src, label); ok {
			s.Family = Linux
			return s, true
		}
	}
	if !hasLinuxMarkers(src) {
		return System{}, false
	}
	s := System{Family: Linux, Name: string(label)}
	if s.Name == "" {
		s.Name = "Linux"
	}
	s.Arch = archFromTokens(label.words())
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
func fromTreeinfo(src source, _ volumeLabel) (System, bool) {
	data, err := src.readFile(".treeinfo", maxInfoFile)
	if err != nil {
		return System{}, false
	}
	ini := parseINI(data)
	name := first(ini["release.name"], ini["general.family"], ini["product.name"])
	if name == "" {
		return System{}, false
	}
	return System{
		Name:    withVariant(name, first(ini["general.variant"], ini["variant.name"])),
		Version: first(ini["release.version"], ini["general.version"], ini["product.version"]),
		Arch:    normArch(first(ini["tree.arch"], ini["general.arch"])),
	}, true
}

// withVariant appends a .treeinfo variant to the distribution name unless it
// adds nothing: the netinstall "Everything" variant, or one the name already
// contains.
func withVariant(name, variant string) string {
	if variant == "" || variant == "Everything" {
		return name
	}
	if strings.Contains(name, variant) {
		return name
	}
	return name + " " + variant
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
func fromDiskInfo(src source, _ volumeLabel) (System, bool) {
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
	if len(words) == 0 {
		return s, ""
	}
	i := slices.IndexFunc(words[1:], startsWithDigit)
	if i < 0 {
		return s, ""
	}
	return strings.Join(words[:i+1], " "), strings.Join(words[i+1:], " ")
}

func startsWithDigit(s string) bool { return s != "" && isDigit(s[0]) }

func isDigit(b byte) bool { return '0' <= b && b <= '9' }

// fromDiscinfo reads .discinfo: a timestamp, the release ("Fedora 40" or just
// "40"), and the architecture, one per line.
func fromDiscinfo(src source, _ volumeLabel) (System, bool) {
	data, err := src.readFile(".discinfo", maxInfoFile)
	if err != nil {
		return System{}, false
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) < 3 {
		return System{}, false
	}
	// A release line that is only a number ("40") does not name the system.
	name, version := splitVersion(clean(lines[1]))
	if name == "" || startsWithDigit(name) {
		return System{}, false
	}
	return System{Name: name, Version: version, Arch: normArch(clean(lines[2]))}, true
}

// fromArchLabel recognizes Arch Linux media, labelled ARCH_YYYYMM.
func fromArchLabel(src source, label volumeLabel) (System, bool) {
	date, ok := strings.CutPrefix(string(label), "ARCH_")
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
func fromFedoraLabel(_ source, label volumeLabel) (System, bool) {
	words := label.words()
	if len(words) == 0 || !strings.EqualFold(words[0], "fedora") {
		return System{}, false
	}
	name := "Fedora"
	if variant := fedoraVariant(words[1:]); variant != "" {
		name += " " + variant
	}
	return System{Name: name, Version: fedoraRelease(words[1:]), Arch: archFromTokens(words)}, true
}

// fedoraVariant expands the first variant abbreviation in the label words.
func fedoraVariant(words []string) string {
	for _, w := range words {
		if variant := fedoraVariants[strings.ToLower(w)]; variant != "" {
			return variant
		}
	}
	return ""
}

// fedoraRelease is the first two-digit label word: the release number.
func fedoraRelease(words []string) string {
	if i := slices.IndexFunc(words, isFedoraRelease); i >= 0 {
		return words[i]
	}
	return ""
}

func isFedoraRelease(w string) bool { return len(w) == 2 && isDigits(w) }

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

// isDigitOrDot reports the runes a dotted version number is made of.
func isDigitOrDot(r rune) bool { return strings.ContainsRune("0123456789.", r) }

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
func fromDistroLabel(src source, label volumeLabel) (System, bool) {
	words := label.words()
	name, ok := distroName(words)
	if !ok || !hasLinuxMarkers(src) {
		return System{}, false
	}
	fields := parseLabelFields(words[1:])
	if len(fields.variant) != 0 {
		name += " " + strings.Join(fields.variant, " ")
	}
	return System{Name: name, Version: fields.version(), Arch: archFromTokens(words)}, true
}

// distroName looks the label's first word up in distroLabels.
func distroName(words []string) (string, bool) {
	if len(words) == 0 {
		return "", false
	}
	name, ok := distroLabels[strings.ToLower(words[0])]
	return name, ok
}

// labelFields are the version and variant words of a distribution label,
// such as 10, 2 and KDE in Rocky-10-2-KDE.
type labelFields struct {
	numbers []string
	lts     bool
	variant []string
}

func parseLabelFields(words []string) labelFields {
	var f labelFields
	for _, w := range words {
		f.add(w)
	}
	return f
}

// add files one label word. Noise and architecture words are dropped, an LTS
// marker counts only after a version number, and at most two variant words
// are kept.
func (f *labelFields) add(w string) {
	switch {
	case isLabelNoise(w):
	case isVersion(w):
		f.addNumber(w)
	case strings.EqualFold(w, "lts") && len(f.numbers) != 0:
		f.lts = true
	case len(f.variant) < 2:
		f.variant = append(f.variant, w)
	}
}

// addNumber keeps only the numbers before the variant: in Rocky-10-2-KDE-1
// the trailing 1 is a respin, not part of the version.
func (f *labelFields) addNumber(w string) {
	if len(f.variant) == 0 {
		f.numbers = append(f.numbers, w)
	}
}

// version joins the numbers with dots (Rocky-10-2 is 10.2) and appends LTS.
func (f labelFields) version() string {
	if len(f.numbers) == 0 {
		return ""
	}
	v := strings.Join(f.numbers, ".")
	if f.lts {
		v += " LTS"
	}
	return v
}

func isLabelNoise(w string) bool { return normArch(w) != "" || labelNoise[strings.ToLower(w)] }

// isVersion reports a word made of digits and dots that starts with a digit,
// such as 10 or 24.04.1.
func isVersion(s string) bool {
	return startsWithDigit(s) && strings.IndexFunc(s, func(r rune) bool { return !isDigitOrDot(r) }) < 0
}
