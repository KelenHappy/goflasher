//go:build wimlib && !windows

package native

/*
#include <stdlib.h>
#include <wimlib.h>
*/
import "C"

import "unsafe"

// newTString copies s into C memory as a wimlib_tchar string, which is UTF-8
// char outside Windows. The caller frees it with C.free.
func newTString(s string) (*C.wimlib_tchar, bool) {
	return (*C.wimlib_tchar)(unsafe.Pointer(C.CString(s))), true
}

// goTString copies a NUL-terminated wimlib_tchar string into Go memory.
func goTString(s *C.wimlib_tchar) string {
	return C.GoString((*C.char)(unsafe.Pointer(s)))
}
