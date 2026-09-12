//go:build fyne && !windows

package main

// systemScaleFactor is 1 away from Windows: macOS scales at the texture level
// and Fyne's own DPI detection is disabled by applyUIScale elsewhere, so the
// chosen ratio already is the rendered scale.
func systemScaleFactor() float64 {
	return 1
}
