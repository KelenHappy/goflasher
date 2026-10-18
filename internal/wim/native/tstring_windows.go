//go:build wimlib

package native

/*
#include <stdlib.h>
#include <wimlib.h>
*/
import "C"

import (
	"syscall"
	"unsafe"
)

// newTString copies s into C memory as a wimlib_tchar string, which is UTF-16
// wchar_t on Windows, encoded the same way as the os package encodes paths.
// It fails only for an embedded NUL. The caller frees it with C.free.
func newTString(s string) (*C.wimlib_tchar, bool) {
	units, err := syscall.UTF16FromString(s)
	if err != nil {
		return nil, false
	}
	size := C.size_t(len(units)) * C.size_t(unsafe.Sizeof(units[0]))
	buf := C.malloc(size)
	copy(unsafe.Slice((*uint16)(buf), len(units)), units)
	return (*C.wimlib_tchar)(buf), true
}

// goTString copies a NUL-terminated wimlib_tchar string into Go memory.
func goTString(s *C.wimlib_tchar) string {
	if s == nil {
		return ""
	}
	p := unsafe.Pointer(s)
	n := 0
	for *(*uint16)(unsafe.Add(p, 2*n)) != 0 {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(p), n))
}
