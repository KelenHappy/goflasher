//go:build darwin

package native

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	cfStringEncodingUTF8 = 0x08000100
	cfNumberSInt64Type   = 4
)

type cfAPI struct {
	release            func(uintptr)
	retain             func(uintptr) uintptr
	stringCreate       func(uintptr, *byte, uint32) uintptr
	stringLength       func(uintptr) int64
	stringMaxSize      func(int64, uint32) int64
	stringGetCString   func(uintptr, *byte, int64, uint32) bool
	numberGetValue     func(uintptr, int32, unsafe.Pointer) bool
	numberCreate       func(uintptr, int32, unsafe.Pointer) uintptr
	booleanGetValue    func(uintptr) bool
	dictionaryGetValue func(uintptr, uintptr) uintptr
	getTypeID          func(uintptr) uintptr
	stringTypeID       func() uintptr
	numberTypeID       func() uintptr
	booleanTypeID      func() uintptr
	runLoopGetCurrent  func() uintptr
	runLoopRunInMode   func(uintptr, float64, bool) int32
}
type cfBindings struct {
	api                cfAPI
	defaultRunLoopMode uintptr
}

func bindCF(lib uintptr) (cfBindings, error) {
	var b cfBindings
	purego.RegisterLibFunc(&b.api.release, lib, "CFRelease")
	purego.RegisterLibFunc(&b.api.retain, lib, "CFRetain")
	purego.RegisterLibFunc(&b.api.stringCreate, lib, "CFStringCreateWithCString")
	purego.RegisterLibFunc(&b.api.stringLength, lib, "CFStringGetLength")
	purego.RegisterLibFunc(&b.api.stringMaxSize, lib, "CFStringGetMaximumSizeForEncoding")
	purego.RegisterLibFunc(&b.api.stringGetCString, lib, "CFStringGetCString")
	purego.RegisterLibFunc(&b.api.numberGetValue, lib, "CFNumberGetValue")
	purego.RegisterLibFunc(&b.api.numberCreate, lib, "CFNumberCreate")
	purego.RegisterLibFunc(&b.api.booleanGetValue, lib, "CFBooleanGetValue")
	purego.RegisterLibFunc(&b.api.dictionaryGetValue, lib, "CFDictionaryGetValue")
	purego.RegisterLibFunc(&b.api.getTypeID, lib, "CFGetTypeID")
	purego.RegisterLibFunc(&b.api.stringTypeID, lib, "CFStringGetTypeID")
	purego.RegisterLibFunc(&b.api.numberTypeID, lib, "CFNumberGetTypeID")
	purego.RegisterLibFunc(&b.api.booleanTypeID, lib, "CFBooleanGetTypeID")
	purego.RegisterLibFunc(&b.api.runLoopGetCurrent, lib, "CFRunLoopGetCurrent")
	purego.RegisterLibFunc(&b.api.runLoopRunInMode, lib, "CFRunLoopRunInMode")
	var err error
	b.defaultRunLoopMode, err = symbolValue(lib, "kCFRunLoopDefaultMode")
	return b, err
}

func (c cfBindings) newString(s string) (uintptr, error) {
	z := append([]byte(s), 0)
	r := c.api.stringCreate(0, &z[0], cfStringEncodingUTF8)
	if r == 0 {
		return 0, errors.New("CFStringCreateWithCString returned NULL")
	}
	return r, nil
}
func (c cfBindings) goString(v uintptr) (string, bool) {
	if v == 0 || c.api.getTypeID(v) != c.api.stringTypeID() {
		return "", false
	}
	n := c.api.stringMaxSize(c.api.stringLength(v), cfStringEncodingUTF8) + 1
	if n <= 0 {
		return "", false
	}
	b := make([]byte, n)
	if !c.api.stringGetCString(v, &b[0], n, cfStringEncodingUTF8) {
		return "", false
	}
	for i, x := range b {
		if x == 0 {
			return string(b[:i]), true
		}
	}
	return "", false
}
func (c cfBindings) goUint64(v uintptr) (uint64, bool) {
	if v == 0 || c.api.getTypeID(v) != c.api.numberTypeID() {
		return 0, false
	}
	var n int64
	if !c.api.numberGetValue(v, cfNumberSInt64Type, unsafe.Pointer(&n)) || n < 0 {
		return 0, false
	}
	return uint64(n), true
}
func (c cfBindings) goBool(v uintptr) (bool, bool) {
	if v == 0 || c.api.getTypeID(v) != c.api.booleanTypeID() {
		return false, false
	}
	return c.api.booleanGetValue(v), true
}
func (c cfBindings) dictionaryValue(d, key uintptr) uintptr {
	if d == 0 || key == 0 {
		return 0
	}
	return c.api.dictionaryGetValue(d, key)
}
func (c cfBindings) requireString(d, key uintptr, name string) (string, error) {
	v, ok := c.goString(c.dictionaryValue(d, key))
	if !ok {
		return "", fmt.Errorf("%s is not a CFString", name)
	}
	return v, nil
}
