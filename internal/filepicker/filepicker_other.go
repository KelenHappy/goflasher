//go:build !linux

package filepicker

import "errors"

// ErrUnsupported is returned until a platform-native picker is wired in.
var ErrUnsupported = errors.New("native image chooser is not implemented on this platform")

// OpenImage keeps the UI-facing API portable. Windows support can be added in
// this platform file without importing Linux D-Bus dependencies.
func OpenImage(_, _, _ string) (string, error) { return "", ErrUnsupported }
