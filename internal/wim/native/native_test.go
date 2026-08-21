//go:build !windows

package native

import "testing"

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
	message := append([]byte("bad WIM"), 0)
	library := &Library{fn: functions{errorString: func(int32) uintptr { return makeNativeStringPointer(message) }}}
	if got := library.nativeError("open", 17).Error(); got == "" || got == "unknown libwim error" {
		t.Fatalf("error=%q", got)
	}
}

func TestSplitPartSizeUsesBundledUint64ABI(t *testing.T) {
	const partSize = uint64(5) << 30 // deliberately exceeds a 32-bit argument
	var got uint64
	library := &Library{fn: functions{split: func(_ uintptr, _ uintptr, size uint64, _ int32) int32 { got = size; return 0 }}}
	image := &Image{lib: library, handle: 42}
	if err := image.Split("/tmp/install.swm", partSize); err != nil {
		t.Fatal(err)
	}
	if got != partSize {
		t.Fatalf("native part size=%d, want %d", got, partSize)
	}
}
