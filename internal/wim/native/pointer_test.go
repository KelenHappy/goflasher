//go:build linux || darwin

package native

import "unsafe"

func makeNativeStringPointer(b []byte) uintptr { return uintptr(unsafe.Pointer(&b[0])) }
