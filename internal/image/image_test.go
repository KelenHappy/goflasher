package image

import (
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
			cmd := exec.Command("xz", "--compress", "--stdout")
			cmd.Stdin = bytes.NewReader(payload)
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			b.Write(out)
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

func TestDetectRejectsMismatchAndUnknown(t *testing.T) {
	for name, data := range map[string][]byte{"disk.img.gz": []byte("not gzip"), "disk.zip": []byte("PK")} {
		path := filepath.Join(t.TempDir(), name)
		os.WriteFile(path, data, 0600)
		if _, err := Detect(path); err == nil {
			t.Fatalf("Detect(%s) succeeded", name)
		}
	}
}
