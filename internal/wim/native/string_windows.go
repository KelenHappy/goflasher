//go:build windows

package native

import (
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

type nativeString struct {
	pointer uintptr
	units   []uint16
}

func makeNativeString(value string) (nativeString, error) {
	u, err := syscall.UTF16FromString(value)
	if err != nil {
		return nativeString{}, ErrInvalidPath
	}
	return nativeString{pointer: uintptr(unsafe.Pointer(&u[0])), units: u}, nil
}
func (s nativeString) keepAlive() { runtime.KeepAlive(s.units) }

//go:nocheckptr
func goCString(pointer uintptr) string {
	if pointer == 0 {
		return ""
	}
	u := make([]uint16, 0, 128)
	for len(u) < maxCStringResultSize {
		v := *(*uint16)(pointerValue(pointer + uintptr(len(u)*2)))
		if v == 0 {
			return string(utf16.Decode(u))
		}
		u = append(u, v)
	}
	return ""
}

func pointerValue(value uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&value)) }
