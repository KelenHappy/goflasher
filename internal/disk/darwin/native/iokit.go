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
	z := append([]byte(bsd), 0)
	m := f.io.matching(0, 0, &z[0])
	if m == 0 {
		return RegistryIdentity{}, fmt.Errorf("IOBSDNameMatching(%q) returned NULL", bsd)
	}
	cur := f.io.getService(0, m)
	if cur == 0 {
		return RegistryIdentity{}, fmt.Errorf("IOServiceGetMatchingService(%q) returned 0", bsd)
	}
	out := RegistryIdentity{}
	for depth := 0; depth < 64 && cur != 0; depth++ {
		var id uint64
		if f.io.registryID(cur, &id) == 0 && out.EntryID == "" {
			out.EntryID = strconv.FormatUint(id, 16)
		}
		buf := make([]byte, 4096)
		plane := append([]byte("IOService"), 0)
		if f.io.getPath(cur, &plane[0], &buf[0]) == 0 && out.Path == "" {
			out.Path = cString(buf)
		}
		name := make([]byte, 128)
		_ = f.io.getName(cur, &name[0])
		n := strings.ToLower(cString(name))
		if strings.Contains(n, "usb") {
			out.USBAncestor = true
		}
		properties := []struct {
			key string
			dst *string
		}{{"USB Vendor Name", &out.Vendor}, {"USB Product Name", &out.Product}, {"Vendor Identification", &out.Vendor}, {"Product Name", &out.Product}}
		if depth == 0 {
			properties = append(properties,
				struct {
					key string
					dst *string
				}{"UUID", &out.MediaID},
				struct {
					key string
					dst *string
				}{"GUID", &out.MediaID},
				struct {
					key string
					dst *string
				}{"Media UUID", &out.MediaID})
		} else if out.USBAncestor {
			properties = append(properties, struct {
				key string
				dst *string
			}{"USB Serial Number", &out.TransportSerial})
		}
		for _, p := range properties {
			{
				if *p.dst != "" {
					continue
				}
				k, e := f.cf.newString(p.key)
				if e != nil {
					continue
				}
				v := f.io.createProperty(cur, k, 0, 0)
				f.cf.api.release(k)
				if v != 0 {
					if s, ok := f.cf.goString(v); ok {
						*p.dst = s
					}
					f.cf.api.release(v)
				}
			}
		}
		var parent uint32
		status := f.io.getParent(cur, &plane[0], &parent)
		f.io.release(cur)
		cur = 0
		if status != 0 {
			break
		}
		cur = parent
	}
	return out, nil
}
func cString(b []byte) string {
	for i, v := range b {
		if v == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
