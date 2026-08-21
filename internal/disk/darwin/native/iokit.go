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

type registryProperty struct {
	key string
	dst *string
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
	for depth := 0; depth < 64 && cur != 0; depth++ {
		f.readRegistryEntry(cur, depth, &out)
		cur = f.registryParent(cur)
	}
	return out, nil
}

func (f *Frameworks) registryService(bsd string) (uint32, error) {
	name := append([]byte(bsd), 0)
	matching := f.io.matching(0, 0, &name[0])
	if matching == 0 {
		return 0, fmt.Errorf("IOBSDNameMatching(%q) returned NULL", bsd)
	}
	service := f.io.getService(0, matching)
	if service == 0 {
		return 0, fmt.Errorf("IOServiceGetMatchingService(%q) returned 0", bsd)
	}
	return service, nil
}

func (f *Frameworks) readRegistryEntry(entry uint32, depth int, out *RegistryIdentity) {
	f.readRegistryID(entry, out)
	f.readRegistryPath(entry, out)
	f.detectUSBAncestor(entry, out)
	f.readRegistryProperties(entry, registryProperties(depth, out))
}

func (f *Frameworks) readRegistryID(entry uint32, out *RegistryIdentity) {
	var id uint64
	if out.EntryID == "" && f.io.registryID(entry, &id) == 0 {
		out.EntryID = strconv.FormatUint(id, 16)
	}
}

func (f *Frameworks) readRegistryPath(entry uint32, out *RegistryIdentity) {
	if out.Path != "" {
		return
	}
	buffer := make([]byte, 4096)
	plane := []byte("IOService\x00")
	if f.io.getPath(entry, &plane[0], &buffer[0]) == 0 {
		out.Path = cString(buffer)
	}
}

func (f *Frameworks) detectUSBAncestor(entry uint32, out *RegistryIdentity) {
	name := make([]byte, 128)
	_ = f.io.getName(entry, &name[0])
	out.USBAncestor = out.USBAncestor || strings.Contains(strings.ToLower(cString(name)), "usb")
}

func registryProperties(depth int, out *RegistryIdentity) []registryProperty {
	properties := []registryProperty{{"USB Vendor Name", &out.Vendor}, {"USB Product Name", &out.Product}, {"Vendor Identification", &out.Vendor}, {"Product Name", &out.Product}}
	if depth == 0 {
		return append(properties, registryProperty{"UUID", &out.MediaID}, registryProperty{"GUID", &out.MediaID}, registryProperty{"Media UUID", &out.MediaID})
	}
	if out.USBAncestor {
		return append(properties, registryProperty{"USB Serial Number", &out.TransportSerial})
	}
	return properties
}

func (f *Frameworks) readRegistryProperties(entry uint32, properties []registryProperty) {
	for _, property := range properties {
		if *property.dst == "" {
			f.readRegistryProperty(entry, property)
		}
	}
}

func (f *Frameworks) readRegistryProperty(entry uint32, property registryProperty) {
	key, err := f.cf.newString(property.key)
	if err != nil {
		return
	}
	value := f.io.createProperty(entry, key, 0, 0)
	f.cf.api.release(key)
	if value == 0 {
		return
	}
	defer f.cf.api.release(value)
	if text, ok := f.cf.goString(value); ok {
		*property.dst = text
	}
}

func (f *Frameworks) registryParent(entry uint32) uint32 {
	plane := []byte("IOService\x00")
	var parent uint32
	status := f.io.getParent(entry, &plane[0], &parent)
	f.io.release(entry)
	if status != 0 {
		return 0
	}
	return parent
}
func cString(b []byte) string {
	for i, v := range b {
		if v == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
