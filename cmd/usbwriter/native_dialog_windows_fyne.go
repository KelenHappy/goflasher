//go:build fyne && windows

package main

import "fyne.io/fyne/v2"

// runNativeDialog runs show, which blocks while a native dialog is open, off
// the UI goroutine so the Fyne window keeps repainting, then reports on it.
// Win32 common dialogs run their own message loop on any thread.
func runNativeDialog(show func() (string, error), done func(string, error)) {
	go func() {
		path, err := show()
		fyne.Do(func() { done(path, err) })
	}()
}
