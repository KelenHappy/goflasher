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
	"testing"

	"github.com/ulikunitz/xz"
)

func TestDetectAndOpen(t *testing.T) {
	payload := bytes.Repeat([]byte("boot-image"), 1024)
	tests := []struct {
		name        string
		compression Compression
		encode      func(*bytes.Buffer)
	}{
		{"disk.img", CompressionNone, func(b *bytes.Buffer) { b.Write(payload) }},
		{"disk.img.gz", CompressionGzip, func(b *bytes.Buffer) { w := gzip.NewWriter(b); w.Write(payload); w.Close() }},
		{"disk.iso.xz", CompressionXZ, func(b *bytes.Buffer) {
			w, err := xz.NewWriter(b)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(payload); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var encoded bytes.Buffer
			tt.encode(&encoded)
			path := filepath.Join(t.TempDir(), tt.name)
			if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			info, err := Detect(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Compression != tt.compression {
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
			want, _ := Checksum(bytes.NewReader(payload))
			if got != want {
				t.Fatalf("checksum = %s, want %s", got, want)
			}
		})
	}
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
	var encoded bytes.Buffer
	w, err := xz.NewWriter(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "disk.raw.xz")
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
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
	if !bytes.Equal(got, payload) {
		t.Fatalf("decoded payload length = %d, want %d", len(got), len(payload))
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
	if got.UncompressedSize != uint64(len(payload)) {
		t.Fatalf("UncompressedSize = %d, want %d", got.UncompressedSize, len(payload))
	}
	if got.SHA256 != hex.EncodeToString(wantSum[:]) {
		t.Fatalf("SHA256 = %q, want %q", got.SHA256, hex.EncodeToString(wantSum[:]))
	}
	if got.Path != info.Path || got.Format != info.Format || got.Compression != info.Compression {
		t.Fatalf("Inspect() changed source metadata: got %+v, input %+v", got, info)
	}
}

func TestInspectedSourceRetainsOriginalFileAcrossPathReplacement(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", symlink), func(t *testing.T) {
			dir := t.TempDir()
			original := filepath.Join(dir, "original.img")
			selected := original
			want := []byte("inspected bytes")
			if err := os.WriteFile(original, want, 0600); err != nil {
				t.Fatal(err)
			}
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
			defer info.CloseSource()
			replacement := filepath.Join(dir, "replacement.img")
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
			} else if err := os.Rename(replacement, selected); err != nil {
				t.Fatal(err)
			}
			r, err := Open(info)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(r)
			_ = r.Close()
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("retained source = %q, err=%v", got, err)
			}
		})
	}
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
