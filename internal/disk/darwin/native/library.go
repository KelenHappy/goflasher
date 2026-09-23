//go:build darwin

package native

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	coreFoundationPath  = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	diskArbitrationPath = "/System/Library/Frameworks/DiskArbitration.framework/DiskArbitration"
	iokitPath           = "/System/Library/Frameworks/IOKit.framework/IOKit"
)

type libraries struct{ cf, da, io uintptr }

var loadOnce sync.Once
var loaded libraries
var loadErr error

func loadLibraries() (libraries, error) {
	loadOnce.Do(func() {
		open := func(path string) (uintptr, error) {
			h, e := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if e != nil {
				return 0, fmt.Errorf("load %s: %w", path, e)
			}
			return h, nil
		}
		var h uintptr
		if h, loadErr = open(coreFoundationPath); loadErr != nil {
			return
		}
		loaded.cf = h
		if h, loadErr = open(diskArbitrationPath); loadErr != nil {
			return
		}
		loaded.da = h
		h, loadErr = open(iokitPath)
		loaded.io = h
	})
	return loaded, loadErr
}

// symbolValue reads the pointer stored at an exported symbol, which is how the
// framework constants (CFStringRef globals such as kDADiskDescriptionMediaSize)
// are fetched. The address comes from dlsym and points into the mapped dylib,
// never into Go memory, so the Go collector neither moves nor frees it; go vet
// still reports the uintptr conversion because it cannot see that provenance.
func symbolValue(lib uintptr, name string) (uintptr, error) {
	p, e := purego.Dlsym(lib, name)
	if e != nil {
		return 0, e
	}
	return *(*uintptr)(unsafe.Pointer(p)), nil
}
