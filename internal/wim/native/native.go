//go:build linux || darwin

// Package native is the Unix-only package that owns libwim native handles.
//
// wimlib 1.14.5 is linked statically through cgo when the binary is built
// with the wimlib build tag; see link_wimlib.go. Without that tag Open
// reports ErrUnsupported and the installer builder stays unavailable.
package native

import (
	"fmt"
	"strings"
	"sync"
)

// functions holds the wimlib entry points as Go values so the handle and
// locking logic below can be tested without linking wimlib. WIMStruct
// pointers are carried as uintptr and never dereferenced on the Go side.
type functions struct {
	// int wimlib_global_init(int)
	globalInit func(int32) int32
	// uint32_t wimlib_get_version(void)
	getVersion func() uint32
	// const tchar *wimlib_get_version_string(void)
	getVersionString func() string
	// int wimlib_open_wim(const tchar *, int, WIMStruct **)
	openWIM func(string, int32) (uintptr, int32)
	// int wimlib_split(WIMStruct *, const tchar *, uint64_t, int)
	split func(uintptr, string, uint64, int32) int32
	// void wimlib_free(WIMStruct *)
	free func(uintptr)
	// void wimlib_global_cleanup(void)
	globalCleanup func()
	// const tchar *wimlib_get_error_string(enum wimlib_error_code)
	errorString func(int32) string
}

type Library struct {
	mu          sync.Mutex
	fn          functions
	initialized bool
	closed      bool
	globalLock  bool
	openImages  uint64
}

var globalMu sync.Mutex

type Image struct {
	mu     sync.Mutex
	lib    *Library
	handle uintptr
	closed bool
}

// Open initializes the wimlib linked into this binary and checks that it is
// the reviewed release.
func Open() (*Library, error) {
	fn, err := linkedFunctions()
	if err != nil {
		return nil, err
	}
	l := &Library{fn: fn}
	globalMu.Lock()
	l.globalLock = true
	if code := l.fn.globalInit(0); code != 0 {
		initErr := l.nativeError("wimlib_global_init", code)
		l.releaseGlobalLock()
		return nil, initErr
	}
	l.initialized = true
	versionCode := l.fn.getVersion()
	versionString := l.fn.getVersionString()
	if versionCode != BundledVersionCode || versionString != BundledVersion {
		l.fn.globalCleanup()
		l.releaseGlobalLock()
		return nil, fmt.Errorf("%w: %w: %s reports version %s (0x%x), need %s (0x%x)", ErrUnsupported, ErrABIMismatch, linkage, versionString, versionCode, BundledVersion, BundledVersionCode)
	}
	return l, nil
}

// checkPath rejects a path that C would silently truncate at an embedded NUL.
func checkPath(path string) error {
	if strings.ContainsRune(path, 0) {
		return ErrInvalidPath
	}
	return nil
}

func (l *Library) OpenWIM(path string) (*Image, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, ErrLibraryClosed
	}
	if err := checkPath(path); err != nil {
		return nil, err
	}
	handle, code := l.fn.openWIM(path, 0)
	if code != 0 || handle == 0 {
		return nil, l.nativeError("wimlib_open_wim", code)
	}
	l.openImages++
	return &Image{lib: l, handle: handle}, nil
}

func (i *Image) Split(output string, partSize uint64) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.handle == 0 {
		return ErrLibraryClosed
	}
	i.lib.mu.Lock()
	defer i.lib.mu.Unlock()
	if i.lib.closed {
		return ErrLibraryClosed
	}
	if err := checkPath(output); err != nil {
		return err
	}
	if code := i.lib.fn.split(i.handle, output, partSize, 0); code != 0 {
		return i.lib.nativeError("wimlib_split", code)
	}
	return nil
}

func (i *Image) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil
	}
	i.closed = true
	if i.handle != 0 {
		i.lib.mu.Lock()
		i.lib.fn.free(i.handle)
		if i.lib.openImages > 0 {
			i.lib.openImages--
		}
		i.lib.mu.Unlock()
		i.handle = 0
	}
	return nil
}

func (l *Library) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	if l.openImages != 0 {
		return fmt.Errorf("%w: %d images remain open", ErrLibraryClosed, l.openImages)
	}
	l.closed = true
	if l.initialized {
		l.fn.globalCleanup()
		l.initialized = false
	}
	l.releaseGlobalLock()
	return nil
}

func (l *Library) releaseGlobalLock() {
	if l.globalLock {
		l.globalLock = false
		globalMu.Unlock()
	}
}

func (l *Library) nativeError(operation string, code int32) error {
	message := "unknown libwim error"
	if l.fn.errorString != nil {
		if value := l.fn.errorString(code); value != "" {
			message = value
		}
	}
	return fmt.Errorf("%w: %s: %s (%d)", ErrUnsupported, operation, message, code)
}
