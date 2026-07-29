package verify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func TestReadBack(t *testing.T) {
	data := []byte("written image")
	sum := sha256.Sum256(data)
	expected := hex.EncodeToString(sum[:])
	got, err := ReadBack(context.Background(), bytes.NewReader(append(data, []byte("unused capacity")...)), uint64(len(data)), expected, nil)
	if err != nil || got != expected {
		t.Fatalf("got %s, error %v", got, err)
	}
}
func TestReadBackMismatchAndTruncation(t *testing.T) {
	for _, data := range [][]byte{[]byte("different"), []byte("short")} {
		_, err := ReadBack(context.Background(), bytes.NewReader(data), 10, "bad", nil)
		if !errors.Is(err, ErrMismatch) {
			t.Fatalf("error = %v", err)
		}
	}
}
