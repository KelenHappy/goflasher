//go:build darwin

package native

import "unsafe"

func unsafePointer(v uintptr) unsafe.Pointer { return unsafe.Pointer(v) }
