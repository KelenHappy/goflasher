package wim

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverPartsRequiresCanonicalContiguousNonemptyOutput(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
		ok    bool
	}{
		{"canonical", map[string]string{"install.swm": "one", "install2.swm": "two"}, true},
		{"gap", map[string]string{"install.swm": "one", "install3.swm": "three"}, false},
		{"uppercase", map[string]string{"Install.swm": "one"}, false},
		{"leading zero", map[string]string{"install.swm": "one", "install02.swm": "two"}, false},
		{"empty", map[string]string{"install.swm": ""}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parts, err := discoverParts(writeParts(t, tt.files))
			if tt.ok {
				assertPartsAccepted(t, parts, err, len(tt.files))
				return
			}
			assertPartsRejected(t, parts, err)
		})
	}
}

// writeParts materializes files in a fresh temp directory and returns its path.
func writeParts(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func assertPartsAccepted(t *testing.T, parts []Part, err error, want int) {
	t.Helper()
	if err != nil {
		t.Fatalf("error=%v, want accepted", err)
	}
	if len(parts) != want {
		t.Fatalf("parts=%v, want %d parts", parts, want)
	}
}

func assertPartsRejected(t *testing.T, parts []Part, err error) {
	t.Helper()
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("parts=%v error=%v, want %v", parts, err, ErrUnsupported)
	}
}
