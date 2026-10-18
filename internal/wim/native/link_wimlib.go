//go:build (linux || darwin) && wimlib

package native

/*
#cgo LDFLAGS: -lwim
#include <stdint.h>
#include <stdlib.h>
#include <wimlib.h>

#if WIMLIB_MAJOR_VERSION != 1 || WIMLIB_MINOR_VERSION != 14 || WIMLIB_PATCH_VERSION != 5
#error "GoFlasher links only the reviewed wimlib 1.14.5"
#endif

// WIMStruct pointers cross into Go as uintptr_t so Go never converts an
// integer back into a pointer.
static int goflasher_open_wim(const char *path, int flags, uintptr_t *wim) {
	WIMStruct *opened = NULL;
	int code = wimlib_open_wim(path, flags, &opened);
	*wim = (uintptr_t)opened;
	return code;
}

// Defined in Go by progress_wimlib.go. kind uses splitEventKind's values.
extern int goflasherSplitProgress(uintptr_t handle, int kind, uint64_t completed, uint64_t total);

static enum wimlib_progress_status goflasher_split_progress(enum wimlib_progress_msg msg, union wimlib_progress_info *info, void *progctx) {
	int kind;
	uint64_t completed, total;
	switch (msg) {
	case WIMLIB_PROGRESS_MSG_SPLIT_BEGIN_PART:
	case WIMLIB_PROGRESS_MSG_SPLIT_END_PART:
		kind = msg == WIMLIB_PROGRESS_MSG_SPLIT_BEGIN_PART ? 1 : 2;
		completed = info->split.completed_bytes;
		total = info->split.total_bytes;
		break;
	case WIMLIB_PROGRESS_MSG_WRITE_STREAMS:
		kind = 3;
		completed = info->write_streams.completed_compressed_bytes;
		total = info->write_streams.total_bytes;
		break;
	default:
		return WIMLIB_PROGRESS_STATUS_CONTINUE;
	}
	if (!goflasherSplitProgress((uintptr_t)progctx, kind, completed, total))
		return WIMLIB_PROGRESS_STATUS_ABORT;
	return WIMLIB_PROGRESS_STATUS_CONTINUE;
}

// progress is a runtime/cgo.Handle, or 0 for no progress reporting. It is
// registered only for this call so no handle outlives it.
static int goflasher_split(uintptr_t wim, const char *output, uint64_t part_size, int flags, uintptr_t progress) {
	if (progress != 0)
		wimlib_register_progress_function((WIMStruct *)wim, goflasher_split_progress, (void *)progress);
	int code = wimlib_split((WIMStruct *)wim, output, part_size, flags);
	if (progress != 0)
		wimlib_register_progress_function((WIMStruct *)wim, NULL, NULL);
	return code;
}

static void goflasher_free(uintptr_t wim) {
	wimlib_free((WIMStruct *)wim);
}
*/
import "C"

import (
	"runtime/cgo"
	"unsafe"
)

// linkage names this build's wimlib in diagnostics. packaging/legal's
// verify-release.sh also searches release binaries for this exact text to
// detect that wimlib is linked in, so keep the two in step.
const linkage = "statically linked wimlib"

func linkedFunctions() (functions, error) {
	return functions{
		globalInit:       func(flags int32) int32 { return int32(C.wimlib_global_init(C.int(flags))) },
		getVersion:       func() uint32 { return uint32(C.wimlib_get_version()) },
		getVersionString: func() string { return C.GoString(C.wimlib_get_version_string()) },
		openWIM:          openWIM,
		split:            split,
		free:             func(wim uintptr) { C.goflasher_free(C.uintptr_t(wim)) },
		globalCleanup:    func() { C.wimlib_global_cleanup() },
		errorString: func(code int32) string {
			return C.GoString(C.wimlib_get_error_string(C.enum_wimlib_error_code(code)))
		},
	}, nil
}

func openWIM(path string, flags int32) (uintptr, int32) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var wim C.uintptr_t
	code := C.goflasher_open_wim(cpath, C.int(flags), &wim)
	return uintptr(wim), int32(code)
}

func split(wim uintptr, output string, partSize uint64, flags int32, progress func(splitEvent) bool) int32 {
	coutput := C.CString(output)
	defer C.free(unsafe.Pointer(coutput))
	var handle C.uintptr_t
	if progress != nil {
		h := cgo.NewHandle(progress)
		defer h.Delete()
		handle = C.uintptr_t(h)
	}
	return int32(C.goflasher_split(C.uintptr_t(wim), coutput, C.uint64_t(partSize), C.int(flags), handle))
}
