package image

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ulikunitz/xz"
)

func TestDetectAndOpen(t *testing.T) {
	payload := bytes.Repeat([]byte("boot-image"), 1024)
	tests := []struct {
		name        string
		compression Compression
	}{
		{"disk.img", CompressionNone},
		{"disk.img.gz", CompressionGzip},
		{"disk.iso.xz", CompressionXZ},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDetectAndOpen(t, tt.name, tt.compression, payload)
		})
	}
}

func testDetectAndOpen(t *testing.T, name string, compression Compression, payload []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, encodeTestImage(t, compression, payload), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := Detect(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Compression != compression {
		t.Fatalf("compression = %s", info.Compression)
	}
	r, err := Open(info)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := Checksum(r)
	if err != nil {
		t.Fatal(err)
	}
	want := mustChecksum(t, bytes.NewReader(payload))
	if got != want {
		t.Fatalf("checksum = %s, want %s", got, want)
	}
}

func mustChecksum(t *testing.T, r io.Reader) string {
	t.Helper()
	sum, err := Checksum(r)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func encodeTestImage(t *testing.T, compression Compression, payload []byte) []byte {
	t.Helper()
	var encoded bytes.Buffer
	var writer io.WriteCloser
	switch compression {
	case CompressionNone:
		return payload
	case CompressionGzip:
		writer = gzip.NewWriter(&encoded)
	case CompressionXZ:
		var err error
		writer, err = xz.NewWriter(&encoded)
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unsupported test compression %q", compression)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestOpenRejectsCorruptXZWithoutExternalExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.img.xz")
	// A valid XZ header followed by a deliberately truncated stream verifies
	// that decoding and integrity errors come from the in-process reader.
	if err := os.WriteFile(path, []byte("\xfd7zXZ\x00\x00\x04\xe6\xd6\xb4F"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := Detect(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(info); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Open() error = %v, want ErrUnsupported", err)
	}
}

func TestOpenRejectsMissingXZFooter(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "short file", data: []byte("Y")},
		{name: "wrong magic", data: []byte("not-XX")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "disk.img.xz")
			if err := os.WriteFile(path, tt.data, 0600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if err := requireXZStreamFooter(file); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("requireXZStreamFooter() error = %v, want ErrUnsupported", err)
			}
		})
	}
}

func TestXZRoundTripPureGo(t *testing.T) {
	payload := bytes.Repeat([]byte("pure-go-xz-stream"), 4096)
	path := filepath.Join(t.TempDir(), "disk.raw.xz")
	if err := os.WriteFile(path, encodeTestImage(t, CompressionXZ, payload), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := Detect(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(info)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	assertBytesEqual(t, "decoded payload", got, payload)
}

func assertBytesEqual(t *testing.T, label string, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Fatalf("%s length = %d, want %d", label, len(got), len(want))
	}
}

func TestXZDecodesStandardInteroperabilityFixture(t *testing.T) {
	const encoded = "/Td6WFoAAATm1rRGAgAhARYAAAB0L+WjAQAuR29GbGFzaGVyIHB1cmUgR28gWFogaW50ZXJvcGVyYWJpbGl0eSBmaXh0dXJlLgoAAARKtOSWL6L/AAFHL7BRbzQftvN9AQAAAAAEWVo="
	const want = "GoFlasher pure Go XZ interoperability fixture.\n"
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.img.xz")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := Detect(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(info)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("decoded fixture = %q, want %q", got, want)
	}
}

func TestDetectRejectsMismatchAndUnknown(t *testing.T) {
	for name, data := range map[string][]byte{"disk.img.gz": []byte("not gzip"), "disk.zip": []byte("PK")} {
		path := filepath.Join(t.TempDir(), name)
		os.WriteFile(path, data, 0600)
		if _, err := Detect(path); err == nil {
			t.Fatalf("Detect(%s) succeeded", name)
		}
	}
}

func TestInspect(t *testing.T) {
	payload := bytes.Repeat([]byte("inspect-image"), 257)
	path := filepath.Join(t.TempDir(), "disk.raw")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := Detect(path)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Inspect(info)
	if err != nil {
		t.Fatal(err)
	}
	defer got.CloseSource()
	wantSum := sha256.Sum256(payload)
	assertInspectedContent(t, got, payload, hex.EncodeToString(wantSum[:]))
	assertSourceMetadata(t, got, info)
}

func assertInspectedContent(t *testing.T, got Info, payload []byte, wantSHA256 string) {
	t.Helper()
	if got.UncompressedSize != uint64(len(payload)) {
		t.Fatalf("UncompressedSize = %d, want %d", got.UncompressedSize, len(payload))
	}
	if got.SHA256 != wantSHA256 {
		t.Fatalf("SHA256 = %q, want %q", got.SHA256, wantSHA256)
	}
}

func assertSourceMetadata(t *testing.T, got, want Info) {
	t.Helper()
	gotMetadata := [3]string{got.Path, string(got.Format), string(got.Compression)}
	wantMetadata := [3]string{want.Path, string(want.Format), string(want.Compression)}
	if gotMetadata != wantMetadata {
		t.Fatalf("Inspect() changed source metadata: got %+v, input %+v", got, want)
	}
}

func TestInspectedSourceRetainsOriginalFileAcrossPathReplacement(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", symlink), func(t *testing.T) {
			testRetainedSourcePathReplacement(t, symlink)
		})
	}
}

func testRetainedSourcePathReplacement(t *testing.T, symlink bool) {
	t.Helper()
	info, selected, want := inspectReplacementFixture(t, symlink)
	defer info.CloseSource()
	replaceInspectedPath(t, selected, symlink)
	assertRetainedSource(t, info, want)
}

func inspectReplacementFixture(t *testing.T, symlink bool) (Info, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	original := filepath.Join(dir, "original.img")
	want := []byte("inspected bytes")
	if err := os.WriteFile(original, want, 0600); err != nil {
		t.Fatal(err)
	}
	selected := original
	if symlink {
		selected = filepath.Join(dir, "selected.img")
		if err := os.Symlink(original, selected); err != nil {
			t.Fatal(err)
		}
	}
	info, err := Inspect(Info{Path: selected, Compression: CompressionNone})
	if err != nil {
		t.Fatal(err)
	}
	return info, selected, want
}

func replaceInspectedPath(t *testing.T, selected string, symlink bool) {
	t.Helper()
	replacement := filepath.Join(filepath.Dir(selected), "replacement.img")
	if err := os.WriteFile(replacement, []byte("attacker bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if symlink {
		if err := os.Remove(selected); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(replacement, selected); err != nil {
			t.Fatal(err)
		}
		return
	}
	err := os.Rename(replacement, selected)
	if err == nil {
		return
	}
	// Windows may deny replacement while the retained handle is open. Preventing
	// the pathname race entirely is an equally safe outcome for this test.
	if runtime.GOOS == "windows" && errors.Is(err, os.ErrPermission) {
		return
	}
	t.Fatal(err)
}

func assertRetainedSource(t *testing.T, info Info, want []byte) {
	t.Helper()
	r, err := Open(info)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	closeErr := r.Close()
	if err != nil {
		t.Fatalf("read retained source: %v", err)
	}
	if closeErr != nil {
		t.Fatalf("close retained source: %v", closeErr)
	}
	assertBytesEqual(t, "retained source", got, want)
}

func TestInspectedSourceRejectsInPlaceChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(path, []byte("original image"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(Info{Path: path, Compression: CompressionNone})
	if err != nil {
		t.Fatal(err)
	}
	defer info.CloseSource()
	if err := os.Truncate(path, 1); err != nil {
		t.Fatal(err)
	}
	if err := info.ValidateSource(); err == nil {
		t.Fatal("truncated retained source passed validation")
	}
}

func TestRetainedSourceChecksumRejectsSameSizeInPlaceChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(path, []byte("authorized"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(Info{Path: path, Compression: CompressionNone})
	if err != nil {
		t.Fatal(err)
	}
	defer info.CloseSource()
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("malicious!"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stat.ModTime(), stat.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := info.VerifySourceContext(context.Background()); err == nil {
		t.Fatal("same-size in-place replacement passed retained checksum")
	}
}

func TestInspectContextCancelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(path, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := InspectContext(ctx, Info{Path: path, Compression: CompressionNone})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("InspectContext() error = %v, want context.Canceled", err)
	}
}
