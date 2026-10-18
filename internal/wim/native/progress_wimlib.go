//go:build wimlib

package native

// This file holds only the exported callback: a cgo file with //export may
// declare but not define C functions, and link_wimlib.go defines several.

/*
#include <stdint.h>
*/
import "C"

import "runtime/cgo"

// goflasherSplitProgress receives one split progress message from the C
// trampoline in link_wimlib.go and reports whether wimlib should continue.
//
//export goflasherSplitProgress
func goflasherSplitProgress(handle C.uintptr_t, kind C.int, completed, total C.uint64_t) C.int {
	progress := cgo.Handle(handle).Value().(func(splitEvent) bool)
	if progress(splitEvent{kind: splitEventKind(kind), completed: uint64(completed), total: uint64(total)}) {
		return 1
	}
	return 0
}
