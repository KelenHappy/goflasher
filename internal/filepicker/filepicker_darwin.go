//go:build darwin

package filepicker

import "github.com/goflasher/goflasher/internal/filepicker/darwin/native"

// OpenImage displays an AppKit NSOpenPanel. Cancellation is represented by an
// empty path and nil error, consistently with the other platform pickers.
func OpenImage(title, acceptLabel, filterName string) (string, error) {
	return native.OpenImage(title, acceptLabel, filterName)
}

// SaveLog displays an AppKit NSSavePanel. AppKit owns the filter UI, so
// filterName is intentionally unused on this platform.
func SaveLog(title, defaultName, filterName string) (string, error) {
	_ = filterName
	return native.SaveLog(title, defaultName)
}
