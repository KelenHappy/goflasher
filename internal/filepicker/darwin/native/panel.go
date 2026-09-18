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

// appKitClasses holds the Objective-C classes the picker needs.
type appKitClasses struct {
	pool, panel, nsString uintptr
}

// OpenImage must create and run AppKit objects on one OS thread. Fyne invokes
// the picker from its UI callback; LockOSThread prevents a Go reschedule while
// the modal AppKit run loop is active.
func OpenImage(title, acceptLabel, filterName string) (string, error) {
	_, _ = acceptLabel, filterName // image validation remains authoritative.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	api, closeLibs, err := loadObjC()
	if err != nil {
		return "", err
	}
	defer closeLibs()

	classes, err := lookupClasses(api)
	if err != nil {
		return "", err
	}
	pool := api.msg0(api.msg0(classes.pool, sel(api, "alloc")), sel(api, "init"))
	if pool == 0 {
		return "", errors.New("create AppKit autorelease pool")
	}
	defer api.msg0(pool, sel(api, "drain"))

	panel, err := newOpenPanel(api, classes, title)
	if err != nil {
		return "", err
	}
	if api.msgInteger(panel, sel(api, "runModal")) != nsModalResponseOK {
		return "", nil
	}
	return selectedPath(api, panel)
}

// loadObjC opens AppKit and the Objective-C runtime and binds every
// objc_msgSend signature the picker uses. The returned func releases both.
func loadObjC() (objcAPI, func(), error) {
	appkit, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return objcAPI{}, nil, fmt.Errorf("load AppKit: %w", err)
	}
	objc, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		_ = purego.Dlclose(appkit)
		return objcAPI{}, nil, fmt.Errorf("load Objective-C runtime: %w", err)
	}
	var api objcAPI
	purego.RegisterLibFunc(&api.getClass, objc, "objc_getClass")
	purego.RegisterLibFunc(&api.selector, objc, "sel_registerName")
	// Objective-C's objc_msgSend ABI is registered with each signature used.
	purego.RegisterLibFunc(&api.msg0, objc, "objc_msgSend")
	purego.RegisterLibFunc(&api.msg1ptr, objc, "objc_msgSend")
	purego.RegisterLibFunc(&api.msg1bool, objc, "objc_msgSend")
	purego.RegisterLibFunc(&api.msgUTF8, objc, "objc_msgSend")
	purego.RegisterLibFunc(&api.msgInteger, objc, "objc_msgSend")
	closeLibs := func() {
		_ = purego.Dlclose(objc)
		_ = purego.Dlclose(appkit)
	}
	return api, closeLibs, nil
}

// lookupClasses resolves every AppKit class the picker needs, naming the first
// one the Objective-C runtime cannot provide.
func lookupClasses(api objcAPI) (appKitClasses, error) {
	var classes appKitClasses
	required := []struct {
		name string
		dst  *uintptr
	}{
		{"NSAutoreleasePool", &classes.pool},
		{"NSOpenPanel", &classes.panel},
		{"NSString", &classes.nsString},
	}
	for _, class := range required {
		*class.dst = api.getClass(cString(class.name))
		if *class.dst == 0 {
			return appKitClasses{}, fmt.Errorf("AppKit class %s is unavailable", class.name)
		}
	}
	return classes, nil
}

// newOpenPanel creates a single-file NSOpenPanel with the optional title.
func newOpenPanel(api objcAPI, classes appKitClasses, title string) (uintptr, error) {
	panel := api.msg0(classes.panel, sel(api, "openPanel"))
	if panel == 0 {
		return 0, errors.New("NSOpenPanel.openPanel returned nil")
	}
	api.msg1bool(panel, sel(api, "setCanChooseFiles:"), true)
	api.msg1bool(panel, sel(api, "setCanChooseDirectories:"), false)
	api.msg1bool(panel, sel(api, "setAllowsMultipleSelection:"), false)
	if title == "" {
		return panel, nil
	}
	nsTitle := api.msg1ptr(classes.nsString, sel(api, "stringWithUTF8String:"), uintptr(unsafe.Pointer(cString(title))))
	if nsTitle == 0 {
		return 0, errors.New("convert file picker title to NSString")
	}
	api.msg1ptr(panel, sel(api, "setTitle:"), nsTitle)
	return panel, nil
}

// selectedPath copies the panel's chosen file system path into Go memory.
func selectedPath(api objcAPI, panel uintptr) (string, error) {
	url := api.msg0(panel, sel(api, "URL"))
	if url == 0 {
		return "", errors.New("NSOpenPanel returned no selected URL")
	}
	path := api.msg0(url, sel(api, "path"))
	if path == 0 {
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
