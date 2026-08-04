//go:build fyne && (windows || darwin)

package main

import (
	"fyne.io/fyne/v2"

	"github.com/goflasher/goflasher/internal/filepicker"
)

func openImage(_ fyne.Window, title, acceptLabel, dismissLabel, filterName string, done func(string, error)) {
	_ = dismissLabel // The operating system owns the native cancel label.
	go func() {
		path, err := filepicker.OpenImage(title, acceptLabel, filterName)
		fyne.Do(func() { done(path, err) })
	}()
}
