//go:build !windows

package native

import "github.com/ebitengine/purego"

func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
}
func closeLibrary(handle uintptr) error                         { return purego.Dlclose(handle) }
func lookupSymbol(handle uintptr, name string) (uintptr, error) { return purego.Dlsym(handle, name) }
