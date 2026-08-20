//go:build linux && fyne

package main

import (
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/storage"
)

func TestImageFileFilterMatches(t *testing.T) {
	filter := &imageFileFilter{suffixes: supportedImageSuffixes}
	directory := filepath.Join(t.TempDir(), "images.iso")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		uri  fyne.URI
		want bool
	}{
		{name: "ISO", uri: storage.NewFileURI("/images/disk.iso"), want: true},
		{name: "IMG", uri: storage.NewFileURI("/images/disk.img"), want: true},
		{name: "RAW", uri: storage.NewFileURI("/images/disk.raw"), want: true},
		{name: "ISO gzip", uri: storage.NewFileURI("/images/disk.iso.gz"), want: true},
		{name: "IMG gzip", uri: storage.NewFileURI("/images/disk.img.gz"), want: true},
		{name: "ISO xz", uri: storage.NewFileURI("/images/disk.iso.xz"), want: true},
		{name: "IMG xz with dotted version", uri: storage.NewFileURI("/images/1.0test.img.xz"), want: true},
		{name: "case insensitive compound suffix", uri: storage.NewFileURI("/images/DISK.ISO.XZ"), want: true},
		{name: "unsupported gzip base", uri: storage.NewFileURI("/images/document.gz"), want: false},
		{name: "unsupported xz base", uri: storage.NewFileURI("/images/backup.xz"), want: false},
		{name: "ZIP", uri: storage.NewFileURI("/images/disk.zip"), want: false},
		{name: "no extension", uri: storage.NewFileURI("/images/disk"), want: false},
		{name: "directory with supported suffix", uri: storage.NewFileURI(directory), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filter.Matches(tt.uri); got != tt.want {
				t.Errorf("Matches(%q) = %t, want %t", tt.uri, got, tt.want)
			}
		})
	}
}
