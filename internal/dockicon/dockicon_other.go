//go:build !darwin

package dockicon

// Set does nothing: the window icon set through Fyne already applies.
func Set([]byte) error { return nil }
