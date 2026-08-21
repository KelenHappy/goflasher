//go:build windows

package native

import "golang.org/x/sys/windows"

func openLibrary(path string) (uintptr, error) {
	handle, err := windows.LoadLibrary(path)
	return uintptr(handle), err
}
func closeLibrary(handle uintptr) error { return windows.FreeLibrary(windows.Handle(handle)) }
func lookupSymbol(handle uintptr, name string) (uintptr, error) {
	return windows.GetProcAddress(windows.Handle(handle), name)
}
