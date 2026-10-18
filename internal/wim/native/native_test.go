//go:build linux || darwin

package native

import (
	"errors"
	"strings"
	"testing"
)

func TestBundledVersionIsPinnedToReviewedRelease(t *testing.T) {
	if BundledVersion != "1.14.5" || BundledVersionCode != uint32((1<<20)|(14<<10)|5) {
		t.Fatalf("bundled version=%s code=%#x", BundledVersion, BundledVersionCode)
	}
}

func TestNativeCloseIsIdempotent(t *testing.T) {
	var freed, cleaned int
	library := &Library{initialized: true, fn: functions{free: func(uintptr) { freed++ }, globalCleanup: func() { cleaned++ }}}
	image := &Image{lib: library, handle: 42}
	if err := image.Close(); err != nil {
		t.Fatal(err)
	}
	if err := image.Close(); err != nil {
		t.Fatal(err)
	}
	if err := library.Close(); err != nil {
		t.Fatal(err)
	}
	if err := library.Close(); err != nil {
		t.Fatal(err)
	}
	if freed != 1 || cleaned != 1 {
		t.Fatalf("free=%d cleanup=%d", freed, cleaned)
	}
}

func TestNativeErrorsUseBundledErrorString(t *testing.T) {
	library := &Library{fn: functions{errorString: func(int32) string { return "bad WIM" }}}
	if got := library.nativeError("open", 17).Error(); !strings.Contains(got, "bad WIM") {
		t.Fatalf("error=%q", got)
	}
}

func TestNativePathsRejectEmbeddedNUL(t *testing.T) {
	called := false
	library := &Library{fn: functions{
		openWIM: func(string, int32) (uintptr, int32) { called = true; return 42, 0 },
		split:   func(uintptr, string, uint64, int32) int32 { called = true; return 0 },
	}}
	if _, err := library.OpenWIM("/tmp/a\x00b.wim"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("OpenWIM error=%v", err)
	}
	image := &Image{lib: library, handle: 42}
	if err := image.Split("/tmp/a\x00b.swm", 1024); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Split error=%v", err)
	}
	if called {
		t.Fatal("NUL-containing path reached wimlib")
	}
}

func TestSplitPartSizeUsesBundledUint64ABI(t *testing.T) {
	const partSize = uint64(5) << 30 // deliberately exceeds a 32-bit argument
	var got uint64
	library := &Library{fn: functions{split: func(_ uintptr, _ string, size uint64, _ int32) int32 { got = size; return 0 }}}
	image := &Image{lib: library, handle: 42}
	if err := image.Split("/tmp/install.swm", partSize); err != nil {
		t.Fatal(err)
	}
	if got != partSize {
		t.Fatalf("native part size=%d, want %d", got, partSize)
	}
}
