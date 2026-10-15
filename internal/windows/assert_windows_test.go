//go:build windows

package windows

import (
	"errors"
	"strings"
	"testing"
)

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func requireError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
}

func requireErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error=%v, want %v", err, target)
	}
}

func requireErrorContains(t *testing.T, err error, substr string) {
	t.Helper()
	requireError(t, err)
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error=%v, want it to mention %q", err, substr)
	}
}

func requireEqual[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s=%v, want %v", what, got, want)
	}
}
