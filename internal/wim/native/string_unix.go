//go:build !windows

package native

import (
	"runtime"
	"strings"
	"unsafe"
)

type nativeString struct {
	pointer uintptr
	bytes   []byte
}

func makeNativeString(value string) (nativeString, error) {
	if strings.ContainsRune(value, 0) {
		return nativeString{}, ErrInvalidPath
	}
	b := append([]byte(value), 0)
	return nativeString{pointer: uintptr(unsafe.Pointer(&b[0])), bytes: b}, nil
}
func (s nativeString) keepAlive() { runtime.KeepAlive(s.bytes) }

//go:nocheckptr
func goCString(pointer uintptr) string {
	if pointer == 0 {
		return ""
	}
	length := 0
	for length < maxCStringResultSize && *(*byte)(pointerValue(pointer + uintptr(length))) != 0 {
		length++
	}
	if length == maxCStringResultSize {
		return ""
	}
	return unsafe.String((*byte)(pointerValue(pointer)), length)
}

func pointerValue(value uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&value)) }
