// Package native contains the Unix bundled-libwim bridge's public errors.
// Its loader implementation is excluded from Windows builds.
package native

import "errors"

const (
	BundledVersion     = "1.14.5"
	BundledVersionCode = uint32((1 << 20) | (14 << 10) | 5)
)

var (
	ErrUnsupported   = errors.New("bundled libwim is unavailable or incompatible")
	ErrABIMismatch   = errors.New("bundled libwim ABI/version mismatch")
	ErrInvalidPath   = errors.New("libwim path is not application-controlled")
	ErrLibraryClosed = errors.New("libwim library is closed")
)
