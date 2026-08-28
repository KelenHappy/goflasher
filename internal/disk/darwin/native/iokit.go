//go:build darwin

package native

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ebitengine/purego"
)

type ioBindings struct {
	matching       func(uint32, uint32, *byte) uintptr
	getService     func(uint32, uintptr) uint32
	getParent      func(uint32, *byte, *uint32) int32
	getName        func(uint32, *byte) int32
	getPath        func(uint32, *byte, *byte) int32
	createProperty func(uint32, uintptr, uintptr, uint32) uintptr
	registryID     func(uint32, *uint64) int32
	release        func(uint32) int32
	cf             cfBindings
}

func bindIOKit(lib uintptr, cf cfBindings) (ioBindings, error) {
	var i ioBindings
	i.cf = cf
	purego.RegisterLibFunc(&i.matching, lib, "IOBSDNameMatching")
	purego.RegisterLibFunc(&i.getService, lib, "IOServiceGetMatchingService")
	purego.RegisterLibFunc(&i.getParent, lib, "IORegistryEntryGetParentEntry")
	purego.RegisterLibFunc(&i.getName, lib, "IORegistryEntryGetName")
	purego.RegisterLibFunc(&i.getPath, lib, "IORegistryEntryGetPath")
	purego.RegisterLibFunc(&i.createProperty, lib, "IORegistryEntryCreateCFProperty")
	purego.RegisterLibFunc(&i.registryID, lib, "IORegistryEntryGetRegistryEntryID")
	purego.RegisterLibFunc(&i.release, lib, "IOObjectRelease")
	return i, nil
}

func (f *Frameworks) RegistryIdentity(bsd string) (RegistryIdentity, error) {
	cur, err := f.registryService(bsd)
	if err != nil {
		return RegistryIdentity{}, err
	}
	out := RegistryIdentity{}
	for depth := 0; depth < 64; depth++ {
		if cur == 0 {
			return out, nil
		}
		cur = f.inspectRegistryEntry(cur, depth, &out)
	}
	// IORegistryEntryGetParentEntry returns a retained object. Release the first
	// ancestor beyond the traversal limit rather than leaking its reference.
	f.releaseRegistryObject(cur)
	return out, nil
}

func (f *Frameworks) releaseRegistryObject(object uint32) {
	if object != 0 {
		f.io.release(object)
	}
}

func (f *Frameworks) registryService(bsd string) (uint32, error) {
	z := append([]byte(bsd), 0)
	m := f.io.matching(0, 0, &z[0])
	if m == 0 {
		return 0, fmt.Errorf("IOBSDNameMatching(%q) returned NULL", bsd)
	}
	cur := f.io.getService(0, m)
	if cur == 0 {
		return 0, fmt.Errorf("IOServiceGetMatchingService(%q) returned 0", bsd)
	}
	return cur, nil
}

func (f *Frameworks) inspectRegistryEntry(cur uint32, depth int, out *RegistryIdentity) uint32 {
	plane := append([]byte("IOService"), 0)
	f.readRegistryEntryID(cur, out)
	f.readRegistryPath(cur, plane, out)
	f.detectUSBAncestor(cur, out)
	f.readRegistryProperties(cur, registryProperties(depth, out))
	var parent uint32
	status := f.io.getParent(cur, &plane[0], &parent)
	f.io.release(cur)
	if status != 0 {
		return 0
	}
	return parent
}

func (f *Frameworks) readRegistryEntryID(cur uint32, out *RegistryIdentity) {
	if out.EntryID != "" {
		return
	}
	var id uint64
	if f.io.registryID(cur, &id) == 0 {
		out.EntryID = strconv.FormatUint(id, 16)
	}
}

func (f *Frameworks) readRegistryPath(cur uint32, plane []byte, out *RegistryIdentity) {
	if out.Path != "" {
		return
	}
	buf := make([]byte, 4096)
	if f.io.getPath(cur, &plane[0], &buf[0]) == 0 {
		out.Path = cString(buf)
	}
}

func (f *Frameworks) detectUSBAncestor(cur uint32, out *RegistryIdentity) {
	name := make([]byte, 128)
	_ = f.io.getName(cur, &name[0])
	if strings.Contains(strings.ToLower(cString(name)), "usb") {
		out.USBAncestor = true
	}
}

type registryProperty struct {
	key string
	dst *string
}

func registryProperties(depth int, out *RegistryIdentity) []registryProperty {
	properties := []registryProperty{
		{"USB Vendor Name", &out.Vendor},
		{"USB Product Name", &out.Product},
		{"Vendor Identification", &out.Vendor},
		{"Product Name", &out.Product},
	}
	if depth == 0 {
		return append(properties,
			registryProperty{"UUID", &out.MediaID},
			registryProperty{"GUID", &out.MediaID},
			registryProperty{"Media UUID", &out.MediaID},
		)
	}
	if out.USBAncestor {
		return append(properties, registryProperty{"USB Serial Number", &out.TransportSerial})
	}
	return properties
}

func (f *Frameworks) readRegistryProperties(cur uint32, properties []registryProperty) {
	for _, property := range properties {
		f.readRegistryProperty(cur, property)
	}
}

func (f *Frameworks) readRegistryProperty(cur uint32, property registryProperty) {
	if *property.dst != "" {
		return
	}
	key, err := f.cf.newString(property.key)
	if err != nil {
		return
	}
	value := f.io.createProperty(cur, key, 0, 0)
	f.cf.api.release(key)
	if value == 0 {
		return
	}
	defer f.cf.api.release(value)
	if text, ok := f.cf.goString(value); ok {
		*property.dst = text
	}
}

func cString(b []byte) string {
	for i, v := range b {
		if v == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
