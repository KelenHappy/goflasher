//go:build linux || darwin

package wim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type fakeLibrary struct {
	image      *fakeImage
	openErr    error
	openCalls  int
	closeCalls int
}

func (f *fakeLibrary) OpenWIM(string) (nativeImage, error) {
	f.openCalls++
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.image, nil
}
func (f *fakeLibrary) Close() error { f.closeCalls++; return nil }

type fakeImage struct {
	output     string
	splitErr   error
	splitCalls int
	closeCalls int
}

func (f *fakeImage) Split(output string, _ uint64) error {
	f.output = output
	f.splitCalls++
	if f.splitErr != nil {
		_ = os.WriteFile(output, []byte("partial"), 0600)
		return f.splitErr
	}
	if err := os.WriteFile(output, []byte("part-one"), 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(filepath.Dir(output), "install2.swm"), []byte("part-two"), 0600)
}
func (f *fakeImage) Close() error { f.closeCalls++; return nil }

func withFakeNative(t *testing.T, fn func(string, string) (nativeLibrary, error)) {
	t.Helper()
	old, oldLocator := openNative, locateBundledLibrary
	openNative = fn
	locateBundledLibrary = func() (string, string, error) {
		return filepath.Join(t.TempDir(), "libwim-test"), t.TempDir(), nil
	}
	t.Cleanup(func() { openNative, locateBundledLibrary = old, oldLocator })
}
func splitPaths(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "install.wim")
	output := filepath.Join(root, "out")
	if err := os.WriteFile(source, []byte("wim"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	return source, output
}

func TestDarwinLibraryPathIsFixedInsideApplicationBundle(t *testing.T) {
	path, root, err := bundledLibraryPathFor("/Applications/GoFlasher.app/Contents/MacOS/GoFlasher", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/Applications/GoFlasher.app/Contents/Frameworks/libwim.15.dylib" || root != "/Applications/GoFlasher.app/Contents" {
		t.Fatalf("path=%q root=%q", path, root)
	}
	if _, _, err := bundledLibraryPathFor("/tmp/GoFlasher", "darwin"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unbundled path error=%v", err)
	}
}

func TestPackagedLinuxLibraryPathIsPrivate(t *testing.T) {
	path, root, err := bundledLibraryPathFor("/usr/bin/goflasher", "linux")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/usr/lib/goflasher/lib/wimlib/1.14.5/libwim.so.15" || root != "/usr/lib/goflasher" {
		t.Fatalf("path=%q root=%q", path, root)
	}
}

func TestSplitRejectsNativeInitializationAndMissingSymbols(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{{"global init failure", errors.New("init")}, {"required symbol missing", errors.New("symbol")}} {
		t.Run(tt.name, func(t *testing.T) {
			source, output := splitPaths(t)
			withFakeNative(t, func(string, string) (nativeLibrary, error) { return nil, tt.err })
			if _, err := Split(context.Background(), source, output, 1024, nil); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestSplitOpenFailureCleansUpLibrary(t *testing.T) {
	source, output := splitPaths(t)
	lib := &fakeLibrary{openErr: errors.New("open")}
	withFakeNative(t, func(string, string) (nativeLibrary, error) { return lib, nil })
	if _, err := Split(context.Background(), source, output, 1024, nil); err == nil {
		t.Fatal("Split succeeded")
	}
	if lib.closeCalls != 1 {
		t.Fatalf("library closes=%d", lib.closeCalls)
	}
}

func TestSplitFailureClosesObjectsAndRemovesPartialParts(t *testing.T) {
	source, output := splitPaths(t)
	image := &fakeImage{splitErr: errors.New("split")}
	lib := &fakeLibrary{image: image}
	withFakeNative(t, func(string, string) (nativeLibrary, error) { return lib, nil })
	if _, err := Split(context.Background(), source, output, 1024, nil); err == nil {
		t.Fatal("Split succeeded")
	}
	assertNativeReleased(t, lib, image)
	if _, err := os.Stat(filepath.Join(output, "install.swm")); !os.IsNotExist(err) {
		t.Fatalf("partial output remains: %v", err)
	}
}

func TestSplitReturnsContiguousPartsAndSynchronousProgress(t *testing.T) {
	source, output := splitPaths(t)
	image := &fakeImage{}
	lib := &fakeLibrary{image: image}
	withFakeNative(t, func(string, string) (nativeLibrary, error) { return lib, nil })
	var progress [][2]uint64
	record := func(done, total uint64) { progress = append(progress, [2]uint64{done, total}) }
	parts, err := Split(context.Background(), source, output, 1024, record)
	if err != nil {
		t.Fatal(err)
	}
	assertPartNames(t, parts, "install.swm", "install2.swm")
	assertProgress(t, progress, [2]uint64{0, 1}, [2]uint64{1, 1})
	assertNativeReleased(t, lib, image)
}

// assertPartNames checks that parts carry exactly the wanted base names, in order.
func assertPartNames(t *testing.T, parts []Part, want ...string) {
	t.Helper()
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		names = append(names, filepath.Base(part.Path))
	}
	if !slices.Equal(names, want) {
		t.Fatalf("parts=%v, want %v", names, want)
	}
}

// assertProgress checks that the callback fired synchronously with exactly the
// wanted done/total pairs, in order.
func assertProgress(t *testing.T, got [][2]uint64, want ...[2]uint64) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("progress=%v, want %v", got, want)
	}
}

// assertNativeReleased checks that Split closed the image and the library once.
func assertNativeReleased(t *testing.T, lib *fakeLibrary, image *fakeImage) {
	t.Helper()
	if image.closeCalls != 1 {
		t.Errorf("image closes=%d, want 1", image.closeCalls)
	}
	if lib.closeCalls != 1 {
		t.Errorf("library closes=%d, want 1", lib.closeCalls)
	}
}
