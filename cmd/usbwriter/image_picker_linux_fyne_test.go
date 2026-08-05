//go:build linux && fyne

package main

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

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
