//go:build fyne && (windows || darwin)

package main

import (
	"fyne.io/fyne/v2"

	"github.com/goflasher/goflasher/internal/filepicker"
)

// openImage ignores labels.Dismiss because the operating system owns the
// native cancel label.
func openImage(_ fyne.Window, labels imagePickerLabels, done func(string, error)) {
	runNativeDialog(func() (string, error) {
		return filepicker.OpenImage(labels.Title, labels.Accept, labels.Filter)
	}, done)
}
