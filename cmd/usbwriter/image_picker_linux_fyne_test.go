//go:build linux && fyne

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/storage"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
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

func TestLinuxImageChooserCanShowBeforeResize(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	window := app.NewWindow("test")
	defer window.Close()

	// Regression: Fyne 2.8 panics in FileDialog.MinSize if Resize is called
	// before Show has initialized the dialog's internal widget tree.
	openImage(window, "Choose an image file", "Choose", "Cancel", "USB images", func(string, error) {})
	defer dismissFileDialog(t, window)
}

func TestLinuxImageChooserRestoresSizeAfterParentGrows(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	window := app.NewWindow("test")
	defer window.Close()
	window.Resize(fyne.NewSize(900, 700))

	openImage(window, "Choose an image file", "Choose", "Cancel", "USB images", func(string, error) {})
	defer dismissFileDialog(t, window)
	popup := findFileDialogPopup(t, window)
	want := popup.Size()

	window.Resize(fyne.NewSize(400, 300))
	layoutTopOverlay(t, window)
	if got := popup.Size(); got.Width >= want.Width || got.Height >= want.Height {
		t.Fatalf("dialog size after shrink = %v, want smaller than %v", got, want)
	}
	window.Resize(fyne.NewSize(900, 700))
	layoutTopOverlay(t, window)

	deadline := time.Now().Add(time.Second)
	for popup.Size() != want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := popup.Size(); got != want {
		t.Errorf("dialog size after parent regrows = %v, want %v", got, want)
	}
}

func findFileDialogPopup(t *testing.T, window fyne.Window) *widget.PopUp {
	t.Helper()
	for _, object := range fynetest.LaidOutObjects(window.Canvas().Overlays().Top()) {
		if popup, ok := object.(*widget.PopUp); ok {
			return popup
		}
	}
	t.Fatal("file dialog popup not found")
	return nil
}

func layoutTopOverlay(t *testing.T, window fyne.Window) {
	t.Helper()
	overlay, ok := window.Canvas().Overlays().Top().(fyne.Widget)
	if !ok {
		t.Fatal("top overlay is not a widget")
	}
	overlay.Resize(window.Canvas().Size())
	fynetest.WidgetRenderer(overlay).Layout(overlay.Size())
}

func dismissFileDialog(t *testing.T, window fyne.Window) {
	t.Helper()
	for _, object := range fynetest.LaidOutObjects(window.Canvas().Overlays().Top()) {
		button, ok := object.(*widget.Button)
		if ok && button.Text == "Cancel" {
			fynetest.Tap(button)
			return
		}
	}
	t.Fatal("file dialog dismiss button not found")
}
