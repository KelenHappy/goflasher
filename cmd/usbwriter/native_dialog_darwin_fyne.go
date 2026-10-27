//go:build fyne && darwin

package main

import "fyne.io/fyne/v2"

// runNativeDialog runs show, which blocks while a native dialog is open, on
// the Fyne UI goroutine. AppKit only creates windows on the main thread, which
// Fyne's GLFW driver keeps locked to that goroutine; an NSOpenPanel created on
// any other thread raises NSInternalInconsistencyException and aborts the app.
// The panel's modal run loop keeps servicing AppKit events meanwhile.
func runNativeDialog(show func() (string, error), done func(string, error)) {
	fyne.Do(func() {
		path, err := show()
		done(path, err)
	})
}
