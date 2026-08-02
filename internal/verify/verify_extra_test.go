package verify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/goflasher/goflasher/internal/progress"
)

// TestReadBackExactSize ensures ReadBack succeeds when the reader contains
// no trailing capacity — i.e. exactly size bytes are available.
func TestReadBackExactSize(t *testing.T) {
	data := []byte("exact size data")
	sum := sha256.Sum256(data)
	expected := hex.EncodeToString(sum[:])

	got, err := ReadBack(context.Background(), bytes.NewReader(data), uint64(len(data)), expected, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != expected {
		t.Fatalf("hash mismatch: got %s, want %s", got, expected)
	}
}

// TestReadBackContextCancellation ensures ReadBack returns the context error
// when the context is already cancelled before the call.
func TestReadBackContextCancellation(t *testing.T) {
	data := []byte("some data that will never be fully read")
	sum := sha256.Sum256(data)
	expected := hex.EncodeToString(sum[:])

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := ReadBack(ctx, bytes.NewReader(data), uint64(len(data)), expected, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestReadBackSendsProgressUpdates ensures ReadBack sends at least one progress
// update to the provided channel during a successful read.
func TestReadBackSendsProgressUpdates(t *testing.T) {
	data := []byte("progress test payload")
	sum := sha256.Sum256(data)
	expected := hex.EncodeToString(sum[:])

	ch := make(chan progress.Update, 64)
	_, err := ReadBack(context.Background(), bytes.NewReader(data), uint64(len(data)), expected, ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ch) == 0 {
		t.Fatal("expected at least one progress update, got none")
	}
	for u := range ch {
		if u.Stage != progress.StageVerifying {
			t.Errorf("unexpected stage: %v", u.Stage)
		}
		if len(ch) == 0 {
			break
		}
	}
}
