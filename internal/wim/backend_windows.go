//go:build windows

package wim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows splits install.wim with WIMGAPI, the Win32 imaging API in
// System32\wimgapi.dll that DISM itself uses, so the Windows build links no
// wimlib and runs no external command.

const (
	// wimGenericRead and wimOpenExisting are WIM_GENERIC_READ and
	// WIM_OPEN_EXISTING from wimgapi.h.
	wimGenericRead  = 0x80000000
	wimOpenExisting = 3

	// wimMsgProgress is WIM_MSG_PROGRESS (WM_APP + 0x1476 + 2); its wParam is
	// the percentage complete.
	wimMsgProgress = 0x8000 + 0x1476 + 2

	// wimMsgSuccess and wimMsgAbortImage are the callback return values that
	// continue and abort the operation.
	wimMsgSuccess    = 0
	wimMsgAbortImage = 0xFFFFFFFF

	// invalidCallbackValue is what WIMRegisterMessageCallback returns on
	// failure.
	invalidCallbackValue = 0xFFFFFFFF
)

var (
	wimgapi                          = windows.NewLazySystemDLL("wimgapi.dll")
	procWIMCreateFile                = wimgapi.NewProc("WIMCreateFile")
	procWIMSetTemporaryPath          = wimgapi.NewProc("WIMSetTemporaryPath")
	procWIMSplitFile                 = wimgapi.NewProc("WIMSplitFile")
	procWIMRegisterMessageCallback   = wimgapi.NewProc("WIMRegisterMessageCallback")
	procWIMUnregisterMessageCallback = wimgapi.NewProc("WIMUnregisterMessageCallback")
	procWIMCloseHandle               = wimgapi.NewProc("WIMCloseHandle")

	wimgapiProcs = []*windows.LazyProc{
		procWIMCreateFile, procWIMSetTemporaryPath, procWIMSplitFile,
		procWIMRegisterMessageCallback, procWIMUnregisterMessageCallback, procWIMCloseHandle,
	}
)

// Go callbacks are a limited resource that is never freed, so one trampoline
// serves every split and finds its handler by the integer pvUserData key.
var (
	callbackOnce     sync.Once
	callbackPtr      uintptr
	callbackHandlers sync.Map // uintptr -> func(msg uint32, wParam uintptr) bool
	callbackSeq      atomic.Uintptr
)

func messageCallback(msg, wParam, _, user uintptr) uintptr {
	handler, ok := callbackHandlers.Load(user)
	if !ok {
		return wimMsgSuccess
	}
	if !handler.(func(uint32, uintptr) bool)(uint32(msg), wParam) {
		return wimMsgAbortImage
	}
	return wimMsgSuccess
}

func backendProbe() error {
	if err := wimgapi.Load(); err != nil {
		return errors.Join(ErrUnsupported, err)
	}
	for _, proc := range wimgapiProcs {
		if err := proc.Find(); err != nil {
			return errors.Join(ErrUnsupported, err)
		}
	}
	return nil
}

func backendSplit(ctx context.Context, req Request) (parts []Part, err error) {
	if err := backendProbe(); err != nil {
		return nil, err
	}
	info, err := os.Stat(req.SourcePath)
	if err != nil {
		return nil, err
	}
	output := partDir(req.OutputDir)
	// WIMGAPI's scratch files go in a private directory that discover skips.
	scratch, err := os.MkdirTemp(req.OutputDir, ".wimgapi-")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(scratch)) }()

	progress := percentProgress{total: uint64(info.Size()), report: req.report}
	if err := splitWithWIMGAPI(ctx, req.SourcePath, scratch, output.firstPart(), req.PartSize, &progress); err != nil {
		output.remove()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	parts, err = output.discover()
	if err != nil {
		output.remove()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		output.remove()
		return nil, err
	}
	progress.finish()
	return parts, nil
}

func splitWithWIMGAPI(ctx context.Context, source, scratch, firstPart string, partSize uint64, progress *percentProgress) (err error) {
	if partSize > 1<<63-1 {
		return fmt.Errorf("%w: invalid part size", ErrUnsupported)
	}
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	scratchPtr, err := windows.UTF16PtrFromString(scratch)
	if err != nil {
		return err
	}
	partPtr, err := windows.UTF16PtrFromString(firstPart)
	if err != nil {
		return err
	}

	// WIMGAPI delivers messages on the thread that called it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var creationResult uint32
	handle, _, callErr := procWIMCreateFile.Call(
		uintptr(unsafe.Pointer(sourcePtr)), wimGenericRead, wimOpenExisting, 0, 0,
		uintptr(unsafe.Pointer(&creationResult)))
	if handle == 0 {
		return wimgapiError("WIMCreateFile", callErr)
	}
	defer func() {
		if ok, _, closeErr := procWIMCloseHandle.Call(handle); ok == 0 {
			err = errors.Join(err, wimgapiError("WIMCloseHandle", closeErr))
		}
	}()

	if ok, _, callErr := procWIMSetTemporaryPath.Call(handle, uintptr(unsafe.Pointer(scratchPtr))); ok == 0 {
		return wimgapiError("WIMSetTemporaryPath", callErr)
	}

	callbackOnce.Do(func() { callbackPtr = windows.NewCallback(messageCallback) })
	key := callbackSeq.Add(1)
	callbackHandlers.Store(key, func(msg uint32, wParam uintptr) bool {
		if msg == wimMsgProgress {
			progress.percent(uint64(wParam))
		}
		return ctx.Err() == nil
	})
	defer callbackHandlers.Delete(key)
	if r, _, callErr := procWIMRegisterMessageCallback.Call(handle, callbackPtr, key); uint32(r) == invalidCallbackValue {
		return wimgapiError("WIMRegisterMessageCallback", callErr)
	}
	defer procWIMUnregisterMessageCallback.Call(handle, callbackPtr)

	size := int64(partSize)
	if ok, _, callErr := procWIMSplitFile.Call(handle, uintptr(unsafe.Pointer(partPtr)), uintptr(unsafe.Pointer(&size)), 0); ok == 0 {
		return wimgapiError("WIMSplitFile", callErr)
	}
	return nil
}

func wimgapiError(operation string, err error) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) || errno == 0 {
		return fmt.Errorf("%s failed", operation)
	}
	return fmt.Errorf("%s: %w", operation, errno)
}
