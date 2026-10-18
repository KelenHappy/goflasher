package native

import (
	"errors"
	"fmt"
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
		split:   func(uintptr, string, uint64, int32, func(splitEvent) bool) int32 { called = true; return 0 },
	}}
	if _, err := library.OpenWIM("/tmp/a\x00b.wim"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("OpenWIM error=%v", err)
	}
	image := &Image{lib: library, handle: 42}
	if err := image.Split("/tmp/a\x00b.swm", 1024, nil); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Split error=%v", err)
	}
	if called {
		t.Fatal("NUL-containing path reached wimlib")
	}
}

func TestSplitPartSizeUsesBundledUint64ABI(t *testing.T) {
	const partSize = uint64(5) << 30 // deliberately exceeds a 32-bit argument
	var got uint64
	library := &Library{fn: functions{split: func(_ uintptr, _ string, size uint64, _ int32, _ func(splitEvent) bool) int32 { got = size; return 0 }}}
	image := &Image{lib: library, handle: 42}
	if err := image.Split("/tmp/install.swm", partSize, nil); err != nil {
		t.Fatal(err)
	}
	if got != partSize {
		t.Fatalf("native part size=%d, want %d", got, partSize)
	}
}

func TestSplitProgressIsMonotonicAcrossParts(t *testing.T) {
	const total = 1000
	events := []splitEvent{
		{kind: splitPartBegin, completed: 0, total: total},
		{kind: splitPartWrite, completed: 300},
		{kind: splitPartWrite, completed: 700}, // compressed estimate overshoots the part
		{kind: splitPartEnd, completed: 600, total: total},
		{kind: splitPartBegin, completed: 600, total: total},
		{kind: splitPartWrite, completed: 200},
		{kind: splitPartWrite, completed: 900}, // clamped to the total
		{kind: splitPartEnd, completed: total, total: total},
	}
	var got []uint64
	library := &Library{fn: functions{split: func(_ uintptr, _ string, _ uint64, _ int32, progress func(splitEvent) bool) int32 {
		for _, e := range events {
			if !progress(e) {
				return 76 // WIMLIB_ERR_ABORTED_BY_PROGRESS
			}
		}
		return 0
	}}}
	image := &Image{lib: library, handle: 42}
	err := image.Split("/tmp/install.swm", 1024, func(done, all uint64) bool {
		if all != total {
			t.Fatalf("total=%d", all)
		}
		got = append(got, done)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{0, 300, 700, 700, 700, 800, 1000, 1000}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("progress=%v, want %v", got, want)
	}
}

func TestSplitProgressCanAbort(t *testing.T) {
	library := &Library{fn: functions{split: func(_ uintptr, _ string, _ uint64, _ int32, progress func(splitEvent) bool) int32 {
		if !progress(splitEvent{kind: splitPartBegin, total: 10}) {
			return 76
		}
		return 0
	}}}
	image := &Image{lib: library, handle: 42}
	if err := image.Split("/tmp/install.swm", 1024, func(uint64, uint64) bool { return false }); err == nil {
		t.Fatal("aborted split reported success")
	}
}
