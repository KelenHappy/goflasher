package image

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
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
	r, err := Open(info)
	if err != nil {
		return // Header validation may reject the truncated stream immediately.
	}
	defer r.Close()
	if _, err := io.Copy(io.Discard, r); err == nil {
		t.Fatal("corrupt XZ stream decoded successfully")
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
