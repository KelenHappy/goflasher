package blockio

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"
)

func TestWriterStreamsArbitraryChunksWithAlignedRequests(t *testing.T) {
	const size = 64 << 10
	a, dev := newTestAligned(t, size, 512)
	fill(dev.data, 200)
	original := append([]byte(nil), dev.data...)
	payload := make([]byte, 20000+123) // not a multiple of the sector size
	rand.New(rand.NewSource(2)).Read(payload)

	w := NewWriter(a)
	writeInRandomChunks(t, w, payload, rand.New(rand.NewSource(3)))
	requireUnchangedFrom(t, dev.data, original, len(payload), "bytes past the stream changed before Flush")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	if !bytes.Equal(dev.data[:len(payload)], payload) {
		t.Fatal("streamed bytes differ")
	}
	requireUnchangedFrom(t, dev.data, original, len(payload), "Flush overwrote bytes after the stream")
}

// writeInRandomChunks feeds payload to w in chunks of 1 to 1500 bytes.
func writeInRandomChunks(t *testing.T, w *Writer, payload []byte, rng *rand.Rand) {
	t.Helper()
	for rest := payload; len(rest) > 0; {
		n := min(len(rest), rng.Intn(1500)+1)
		if got, err := w.Write(rest[:n]); err != nil || got != n {
			t.Fatalf("Write(%d) = %d, %v", n, got, err)
		}
		rest = rest[n:]
	}
}

// requireUnchangedFrom fails with message unless data still equals original
// from offset from onwards.
func requireUnchangedFrom(t *testing.T, data, original []byte, from int, message string) {
	t.Helper()
	if !bytes.Equal(data[from:], original[from:]) {
		t.Fatal(message)
	}
}

func TestWriterFailureIsSticky(t *testing.T) {
	a, dev := newTestAligned(t, 8*512, 512)
	dev.failWriteAt = 1024
	w := NewWriter(a)
	if _, err := w.Write(make([]byte, 2048)); err == nil {
		t.Fatal("device failure was not reported")
	}
	if _, err := w.Write([]byte{1}); err == nil {
		t.Fatal("Write after failure succeeded")
	}
	if err := w.Flush(); err == nil {
		t.Fatal("Flush after failure succeeded")
	}
}

func TestWriterRejectsStreamPastDeviceEnd(t *testing.T) {
	a, _ := newTestAligned(t, 4*512, 512)
	w := NewWriter(a)
	if _, err := w.Write(make([]byte, 2048)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte{1}); err != nil {
		t.Fatalf("partial sector is held, so Write should succeed: %v", err)
	}
	if err := w.Flush(); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("Flush = %v, want ErrOutOfRange", err)
	}
}
