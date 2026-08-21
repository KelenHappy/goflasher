// Package native is the only package that owns libwim native handles.
//
// Function declarations below are transcribed from the bundled wimlib 1.14.5
// include/wimlib.h. C int and enum values are int32, uint64_t is uint64, and
// WIMStruct pointers and TCHAR pointers are represented by uintptr.
package native

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ebitengine/purego"
)

const (
	BundledVersion       = "1.14.5"
	BundledVersionCode   = uint32((1 << 20) | (14 << 10) | 5)
	maxCStringResultSize = 4096
)

var (
	ErrUnsupported   = errors.New("bundled libwim is unavailable or incompatible")
	ErrABIMismatch   = errors.New("bundled libwim ABI/version mismatch")
	ErrInvalidPath   = errors.New("libwim path is not application-controlled")
	ErrLibraryClosed = errors.New("libwim library is closed")
)

type functions struct {
	globalInit       func(int32) int32                           // int wimlib_global_init(int)
	getVersion       func() uint32                               // uint32_t wimlib_get_version(void)
	getVersionString func() uintptr                              // const tchar *wimlib_get_version_string(void)
	openWIM          func(uintptr, int32, *uintptr) int32        // int wimlib_open_wim(const tchar *, int, WIMStruct **)
	split            func(uintptr, uintptr, uint64, int32) int32 // int wimlib_split(WIMStruct *, const tchar *, uint64_t, int)
	free             func(uintptr)                               // void wimlib_free(WIMStruct *)
	globalCleanup    func()                                      // void wimlib_global_cleanup(void)
	errorString      func(int32) uintptr                         // const tchar *wimlib_get_error_string(enum wimlib_error_code)
}

type Library struct {
	mu          sync.Mutex
	handle      uintptr
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

// Open loads only a canonical absolute libraryPath contained by allowedRoot.
// It never asks the dynamic loader to search cwd, LD_LIBRARY_PATH, or system
// library directories.
func Open(libraryPath, allowedRoot string) (*Library, error) {
	canonical, err := controlledPath(libraryPath, allowedRoot)
	if err != nil {
		return nil, err
	}
	handle, err := openLibrary(canonical)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	l := &Library{handle: handle}
	if err := l.bind(); err != nil {
		_ = closeLibrary(handle)
		return nil, err
	}
	globalMu.Lock()
	l.globalLock = true
	if code := l.fn.globalInit(0); code != 0 {
		initErr := l.nativeError("wimlib_global_init", code)
		l.releaseGlobalLock()
		_ = closeLibrary(handle)
		return nil, initErr
	}
	l.initialized = true
	versionCode := l.fn.getVersion()
	versionString := goCString(l.fn.getVersionString())
	if versionCode != BundledVersionCode || versionString != BundledVersion {
		l.fn.globalCleanup()
		l.releaseGlobalLock()
		_ = closeLibrary(handle)
		return nil, fmt.Errorf("%w: %w: got version %s (0x%x), need %s (0x%x)", ErrUnsupported, ErrABIMismatch, versionString, versionCode, BundledVersion, BundledVersionCode)
	}
	return l, nil
}

func controlledPath(name, root string) (string, error) {
	if !filepath.IsAbs(name) || !filepath.IsAbs(root) {
		return "", ErrInvalidPath
	}
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	resolvedName, err := filepath.EvalSymlinks(filepath.Clean(name))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedName)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrInvalidPath
	}
	return resolvedName, nil
}

func (l *Library) bind() error {
	bindings := []struct {
		name string
		out  any
	}{
		{"wimlib_global_init", &l.fn.globalInit},
		{"wimlib_get_version", &l.fn.getVersion},
		{"wimlib_get_version_string", &l.fn.getVersionString},
		{"wimlib_open_wim", &l.fn.openWIM},
		{"wimlib_split", &l.fn.split},
		{"wimlib_free", &l.fn.free},
		{"wimlib_global_cleanup", &l.fn.globalCleanup},
		{"wimlib_get_error_string", &l.fn.errorString},
	}
	for _, binding := range bindings {
		address, err := lookupSymbol(l.handle, binding.name)
		if err != nil || address == 0 {
			return fmt.Errorf("%w: required symbol %s", ErrUnsupported, binding.name)
		}
		purego.RegisterFunc(binding.out, address)
	}
	return nil
}

func (l *Library) OpenWIM(path string) (*Image, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, ErrLibraryClosed
	}
	cpath, err := makeNativeString(path)
	if err != nil {
		return nil, err
	}
	var handle uintptr
	code := l.fn.openWIM(cpath.pointer, 0, &handle)
	cpath.keepAlive()
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
	coutput, err := makeNativeString(output)
	if err != nil {
		return err
	}
	code := i.lib.fn.split(i.handle, coutput.pointer, partSize, 0)
	coutput.keepAlive()
	if code != 0 {
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
	if l.handle != 0 {
		err := closeLibrary(l.handle)
		l.handle = 0
		return err
	}
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
		if value := goCString(l.fn.errorString(code)); value != "" {
			message = value
		}
	}
	return fmt.Errorf("%w: %s: %s (%d)", ErrUnsupported, operation, message, code)
}
