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
			dir := t.TempDir()
			for name, body := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			parts, err := discoverParts(dir)
			if tt.ok && (err != nil || len(parts) != len(tt.files)) {
				t.Fatalf("parts=%v error=%v", parts, err)
			}
			if !tt.ok && !errors.Is(err, ErrUnsupported) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
