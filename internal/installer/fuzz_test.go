package installer

import (
	"testing"

	installeriso "github.com/goflasher/goflasher/internal/installer/iso"
)

func FuzzManifestPathNormalizer(f *testing.F) {
	for _, seed := range []string{"efi/boot/bootx64.efi", "../escape", "/absolute", `a\\..\\b`, "A/é", "a/e\u0301"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if len(name) > 4096 {
			t.Skip()
		}
		_, _ = approveDestinations([]installeriso.Entry{{Path: name, DestinationFATPath: name, Type: installeriso.File}})
	})
}

func FuzzSWMPartName(f *testing.F) {
	for _, seed := range []string{"sources/install.swm", "sources/install2.swm", "sources/install0.swm", "sources/install999999999999999999999.swm"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if len(name) > 1024 {
			t.Skip()
		}
		n, ok := swmSequence(name)
		if ok && n < 1 {
			t.Fatalf("accepted invalid sequence %d", n)
		}
	})
}
