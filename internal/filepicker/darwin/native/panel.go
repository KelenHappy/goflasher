//go:build darwin

// Package native owns every AppKit and Objective-C handle used by the Darwin
// file picker. Only an immutable Go path crosses into the parent package.
package native

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	nsModalResponseOK = 1
)

type objcAPI struct {
	getClass   func(*byte) uintptr
	selector   func(*byte) uintptr
	msg0       func(uintptr, uintptr) uintptr
	msg1ptr    func(uintptr, uintptr, uintptr) uintptr
	msg1bool   func(uintptr, uintptr, bool)
	msgUTF8    func(uintptr, uintptr) *byte
	msgInteger func(uintptr, uintptr) int64
}

// OpenImage must create and run AppKit objects on one OS thread. Fyne invokes
// the picker from its UI callback; LockOSThread prevents a Go reschedule while
// the modal AppKit run loop is active.
func OpenImage(title, acceptLabel, filterName string) (string, error) {
	_, _ = acceptLabel, filterName // image validation remains authoritative.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	appkit, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return "", fmt.Errorf("load AppKit: %w", err)
	}
	defer purego.Dlclose(appkit)
	objc, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return "", fmt.Errorf("load Objective-C runtime: %w", err)
	}
	defer purego.Dlclose(objc)

	var api objcAPI
	purego.RegisterLibFunc(&api.getClass, objc, "objc_getClass")
	purego.RegisterLibFunc(&api.selector, objc, "sel_registerName")
	// Objective-C's objc_msgSend ABI is registered with each signature used.
	purego.RegisterLibFunc(&api.msg0, objc, "objc_msgSend")
	purego.RegisterLibFunc(&api.msg1ptr, objc, "objc_msgSend")
	purego.RegisterLibFunc(&api.msg1bool, objc, "objc_msgSend")
	purego.RegisterLibFunc(&api.msgUTF8, objc, "objc_msgSend")
	purego.RegisterLibFunc(&api.msgInteger, objc, "objc_msgSend")

	poolClass := api.getClass(cString("NSAutoreleasePool"))
	panelClass := api.getClass(cString("NSOpenPanel"))
	stringClass := api.getClass(cString("NSString"))
	if poolClass == 0 || panelClass == 0 || stringClass == 0 {
		return "", errors.New("required AppKit class is unavailable")
	}
	pool := api.msg0(api.msg0(poolClass, sel(api, "alloc")), sel(api, "init"))
	if pool == 0 {
		return "", errors.New("create AppKit autorelease pool")
	}
	defer api.msg0(pool, sel(api, "drain"))

	panel := api.msg0(panelClass, sel(api, "openPanel"))
	if panel == 0 {
		return "", errors.New("NSOpenPanel.openPanel returned nil")
	}
	api.msg1bool(panel, sel(api, "setCanChooseFiles:"), true)
	api.msg1bool(panel, sel(api, "setCanChooseDirectories:"), false)
	api.msg1bool(panel, sel(api, "setAllowsMultipleSelection:"), false)
	if title != "" {
		nsTitle := api.msg1ptr(stringClass, sel(api, "stringWithUTF8String:"), uintptr(unsafe.Pointer(cString(title))))
		if nsTitle == 0 {
			return "", errors.New("convert file picker title to NSString")
		}
		api.msg1ptr(panel, sel(api, "setTitle:"), nsTitle)
	}
	if api.msgInteger(panel, sel(api, "runModal")) != nsModalResponseOK {
		return "", nil
	}
	url := api.msg0(panel, sel(api, "URL"))
	path := api.msg0(url, sel(api, "path"))
	if url == 0 || path == 0 {
		return "", errors.New("NSOpenPanel returned no selected URL")
	}
	z := api.msgUTF8(path, sel(api, "UTF8String"))
	if z == nil {
		return "", errors.New("selected path is not UTF-8")
	}
	return goCString(z), nil
}

func sel(api objcAPI, name string) uintptr { return api.selector(cString(name)) }

func cString(value string) *byte {
	b := append([]byte(value), 0)
	return &b[0]
}

func goCString(p *byte) string {
	const maxPath = 1 << 20
	b := unsafe.Slice(p, maxPath)
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return ""
}
