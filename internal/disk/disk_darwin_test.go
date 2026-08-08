//go:build darwin

package disk

import (
	"context"
	"errors"
	"testing"
)

func TestDarwinManagerOutlineIsExplicitlyUnsupported(t *testing.T) {
	manager := NewManager()
	if _, err := manager.List(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("List error = %v, want ErrUnsupported", err)
	}
}
