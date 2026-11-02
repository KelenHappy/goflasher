package osinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildISO writes files under a fresh tree and masters it with xorriso,
// skipping the test where xorriso is not installed.
func buildISO(t *testing.T, label string, files map[string][]byte) string {
	t.Helper()
	xorriso, err := exec.LookPath("xorriso")
	if err != nil {
		t.Skip("xorriso not installed")
	}
	root := t.TempDir()
	for name, data := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "test.iso")
	cmd := exec.Command(xorriso, "-as", "mkisofs", "-quiet", "-J", "-R", "-V", label, "-o", out, root)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("xorriso: %v\n%s", err, b)
	}
	return out
}

func TestIdentifyFileOnMasteredISOs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		label string
		files map[string][]byte
		want  string
	}{
		{"fedora", "Fedora-WS-Live-44-1-5", map[string][]byte{"LiveOS/squashfs.img": {1}, "EFI/BOOT/grubx64.efi": {1}},
			"Fedora Workstation 44"},
		{"ubuntu", "Ubuntu 24.04.1 LTS amd64", map[string][]byte{".disk/info": []byte(`Ubuntu 24.04.1 LTS "Noble Numbat" - Release amd64 (20240827)`), "casper/vmlinuz": {1}},
			"Ubuntu 24.04.1 LTS (x86_64)"},
		{"windows", "CCCOMA_X64FRE_ZH-TW_DV9", map[string][]byte{"bootmgr": {1}, "sources/boot.wim": {1}, "sources/install.wim": testWIM(win11XML, 0)},
			"Windows 11 24H2 (build 26100.1742, x86_64, zh-TW)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IdentifyFile(buildISO(t, tc.label, tc.files))
			if err != nil {
				t.Fatal(err)
			}
			if got.Summary() != tc.want || got.Label != tc.label {
				t.Fatalf("summary=%q label=%q, want %q", got.Summary(), got.Label, tc.want)
			}
		})
	}
}
