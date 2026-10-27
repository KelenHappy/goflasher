//go:build darwin

package dockicon

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/ebitengine/purego"
)

type objcAPI struct {
	getClass  func(*byte) uintptr
	selector  func(*byte) uintptr
	msg0      func(uintptr, uintptr) uintptr
	msg1ptr   func(uintptr, uintptr, uintptr) uintptr
	msgBytes  func(uintptr, uintptr, *byte, uint) uintptr
	msgSetPtr func(uintptr, uintptr, uintptr)
}

// Set replaces the Dock icon with png, an encoded image NSImage can decode. It
// must run on the main thread after NSApplication exists, such as from Fyne's
// OnStarted lifecycle hook.
func Set(png []byte) error {
	if len(png) == 0 {
		return errors.New("dock icon image is empty")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	api, closeLibs, err := loadObjC()
	if err != nil {
		return err
	}
	defer closeLibs()
	classes := map[string]uintptr{}
	for _, name := range []string{"NSAutoreleasePool", "NSApplication", "NSData", "NSImage"} {
		if classes[name] = api.getClass(cString(name)); classes[name] == 0 {
			return fmt.Errorf("AppKit class %s is unavailable", name)
		}
	}
	pool := api.msg0(api.msg0(classes["NSAutoreleasePool"], sel(api, "alloc")), sel(api, "init"))
	if pool == 0 {
		return errors.New("create AppKit autorelease pool")
	}
	defer api.msg0(pool, sel(api, "drain"))

	// NSData copies the bytes before returning, so Go memory is not retained.
	data := api.msgBytes(classes["NSData"], sel(api, "dataWithBytes:length:"), &png[0], uint(len(png)))
	if data == 0 {
		return errors.New("copy dock icon into NSData")
	}
	image := api.msg1ptr(api.msg0(classes["NSImage"], sel(api, "alloc")), sel(api, "initWithData:"), data)
	if image == 0 {
		return errors.New("decode dock icon image")
	}
	defer api.msg0(image, sel(api, "release"))
	app := api.msg0(classes["NSApplication"], sel(api, "sharedApplication"))
	if app == 0 {
		return errors.New("NSApplication is unavailable")
	}
	api.msgSetPtr(app, sel(api, "setApplicationIconImage:"), image)
	return nil
}

// loadObjC opens AppKit and the Objective-C runtime and binds every
// objc_msgSend signature Set uses. The returned func releases both.
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
	purego.RegisterLibFunc(&api.msg0, objc, "objc_msgSend")
	purego.RegisterLibFunc(&api.msg1ptr, objc, "objc_msgSend")
	purego.RegisterLibFunc(&api.msgBytes, objc, "objc_msgSend")
	purego.RegisterLibFunc(&api.msgSetPtr, objc, "objc_msgSend")
	return api, func() {
		_ = purego.Dlclose(objc)
		_ = purego.Dlclose(appkit)
	}, nil
}

func sel(api objcAPI, name string) uintptr { return api.selector(cString(name)) }

func cString(value string) *byte {
	b := append([]byte(value), 0)
	return &b[0]
}
