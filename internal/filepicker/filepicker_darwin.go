//go:build darwin

package filepicker

import "github.com/goflasher/goflasher/internal/filepicker/darwin/native"

// OpenImage displays an AppKit NSOpenPanel. Cancellation is represented by an
// empty path and nil error, consistently with the other platform pickers.
func OpenImage(title, acceptLabel, filterName string) (string, error) {
	return native.OpenImage(title, acceptLabel, filterName)
}
