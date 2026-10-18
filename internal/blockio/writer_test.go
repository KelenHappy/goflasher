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
	rng := rand.New(rand.NewSource(3))
	for rest := payload; len(rest) > 0; {
		n := min(len(rest), rng.Intn(1500)+1)
		if got, err := w.Write(rest[:n]); err != nil || got != n {
			t.Fatalf("Write(%d) = %d, %v", n, got, err)
		}
		rest = rest[n:]
	}
	if !bytes.Equal(dev.data[len(payload):], original[len(payload):]) {
		t.Fatal("bytes past the stream changed before Flush")
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	if !bytes.Equal(dev.data[:len(payload)], payload) {
		t.Fatal("streamed bytes differ")
	}
	if !bytes.Equal(dev.data[len(payload):], original[len(payload):]) {
		t.Fatal("Flush overwrote bytes after the stream")
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
