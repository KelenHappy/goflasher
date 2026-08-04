//go:build linux && fyne

package main

import (
	"testing"

	fynetest "fyne.io/fyne/v2/test"
)

func TestLinuxImageChooserCanShowBeforeResize(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	window := app.NewWindow("test")
	defer window.Close()

	// Regression: Fyne 2.8 panics in FileDialog.MinSize if Resize is called
	// before Show has initialized the dialog's internal widget tree.
	openImage(window, "Choose an image file", "Choose", "Cancel", "USB images", func(string, error) {})
}
