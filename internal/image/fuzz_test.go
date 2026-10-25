package image

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// maxFuzzInput bounds inputs, and maxFuzzDecoded bounds decompressed output,
// so a compression bomb ends an iteration instead of exhausting memory.
const (
	maxFuzzInput   = 1 << 20
	maxFuzzDecoded = 4 << 20
)

type nopLease struct{}

func (nopLease) Close() error { return nil }

// FuzzClassifyReader feeds arbitrary bytes through the ISO filesystem reader,
// the installer manifest checks, and the hybrid MBR scan.
func FuzzClassifyReader(f *testing.F) {
	f.Add(make([]byte, 17*2048))
	f.Add(installerISO(true, false))
	f.Add(installerISO(false, true))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFuzzInput {
			t.Skip()
		}
		kind, err := classifyReader(bytes.NewReader(data), int64(len(data)), nopLease{})
		if err == nil && kind == UnknownImage {
			t.Fatal("classifyReader returned UnknownImage without an error")
		}
	})
}

// FuzzHybridMBR checks the partition table scan against a source size that
// need not match the bytes read, as with a truncated or padded image.
func FuzzHybridMBR(f *testing.F) {
	f.Add(installerISO(false, true), uint64(40*2048))
	f.Add(make([]byte, 512), uint64(0))
	f.Add(make([]byte, 512), ^uint64(0))
	f.Fuzz(func(t *testing.T, data []byte, sourceSize uint64) {
		if len(data) > maxFuzzInput {
			t.Skip()
		}
		_ = hasValidHybridMBR(bytes.NewReader(data), sourceSize)
	})
}

// FuzzDecodedReader decompresses arbitrary gzip and xz streams the way
// writing an image does.
func FuzzDecodedReader(f *testing.F) {
	payload := []byte("goflasher fuzz payload")
	f.Add(encodeTestImage(f, CompressionGzip, payload), false)
	f.Add(encodeTestImage(f, CompressionXZ, payload), true)
	f.Add([]byte("\xfd7zXZ\x00YZ"), true)
	f.Fuzz(func(t *testing.T, data []byte, useXZ bool) {
		if len(data) > maxFuzzInput {
			t.Skip()
		}
		compression := CompressionGzip
		if useXZ {
			compression = CompressionXZ
		}
		file, err := os.Create(filepath.Join(t.TempDir(), "image"))
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		reader, closer, err := decodedReader(compression, file)
		if err != nil {
			return
		}
		if closer != nil {
			defer closer.Close()
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(reader, maxFuzzDecoded))
	})
}

// FuzzFormatFromName checks that every accepted name maps to a known format
// and compression.
func FuzzFormatFromName(f *testing.F) {
	for _, name := range []string{"disk.iso", "DISK.IMG.XZ", "a.raw.gz", "x.gz", ".iso", "a.iso.zip"} {
		f.Add(name)
	}
	f.Fuzz(func(t *testing.T, name string) {
		format, compression, err := formatFromName(name)
		if err != nil {
			return
		}
		switch format {
		case FormatISO, FormatIMG, FormatRAW:
		default:
			t.Fatalf("formatFromName(%q) format = %q", name, format)
		}
		switch compression {
		case CompressionNone, CompressionGzip, CompressionXZ:
		default:
			t.Fatalf("formatFromName(%q) compression = %q", name, compression)
		}
	})
}
