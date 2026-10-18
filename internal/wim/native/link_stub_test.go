//go:build !wimlib

package native

import (
	"errors"
	"testing"
)

func TestOpenWithoutLinkedWimlibIsUnsupported(t *testing.T) {
	if _, err := Open(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Open error=%v", err)
	}
}
