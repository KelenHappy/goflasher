//go:build !linux && !windows && !darwin

package filepicker

import "errors"

// ErrUnsupported is returned until a platform-native picker is wired in.
var ErrUnsupported = errors.New("native image chooser is not implemented on this platform")

// OpenImage keeps the UI-facing API portable on platforms without a native
// implementation, without importing Linux or Windows dependencies.
func OpenImage(_, _, _ string) (string, error) { return "", ErrUnsupported }
