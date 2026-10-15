//go:build linux

package linux

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// devNumber is a kernel block device number (major:minor).
type devNumber struct{ major, minor uint32 }

func (n devNumber) String() string { return fmt.Sprintf("%d:%d", n.major, n.minor) }

// parseDevNumber parses the "major:minor" form used by sysfs dev attributes
// and the third field of mountinfo.
func parseDevNumber(s string) (devNumber, error) {
	p := strings.Split(s, ":")
	if len(p) != 2 {
		return devNumber{}, errors.New("bad device number")
	}
	a, e := strconv.ParseUint(p[0], 10, 32)
	if e != nil {
		return devNumber{}, e
	}
	c, e := strconv.ParseUint(p[1], 10, 32)
	return devNumber{uint32(a), uint32(c)}, e
}

func readDeviceNumber(path string) (devNumber, error) {
	return parseDevNumber(readTrim(path))
}

// blockDevice is one /sys/class/block entry as the kernel names it.
type blockDevice struct {
	name   string    // kernel name, also the entry under /dev
	link   string    // the /sys/class/block/<name> entry
	real   sysfsPath // link with every symlink resolved
	number devNumber
}

// attr reads one sysfs attribute relative to the class entry.
func (d blockDevice) attr(rel string) string { return readTrim(filepath.Join(d.link, rel)) }

func (d blockDevice) isPartition() bool { return exists(filepath.Join(d.link, "partition")) }

// capacity is the size attribute converted from 512-byte sectors to bytes.
func (d blockDevice) capacity() uint64 { return readUint(filepath.Join(d.link, "size")) * 512 }

// topologyID is the identity of last resort for a disk with neither serial
// nor WWN. Enumeration and the privileged helper must spell it identically.
func (d blockDevice) topologyID() string { return d.number.String() + "@" + string(d.real) }

// sysfsPath is a resolved device path under /sys/devices.
type sysfsPath string

func (p sysfsPath) underUSB() bool { return strings.Contains(string(p), "/usb") }

// Some USB flash drives expose their serial only on the USB device node, while
// udev propagates it to ID_SERIAL_SHORT for the block device. Walk to the first
// physical USB ancestor so privileged revalidation uses the same hardware
// identity without trusting data supplied by the GUI.
func (p sysfsPath) usbAncestorAttribute(attribute string) string {
	ancestor := p.usbAncestor()
	if ancestor == "" {
		return ""
	}
	if !ancestor.hasUSBDeviceIdentifiers() {
		return ""
	}
	return readTrim(filepath.Join(string(ancestor), attribute))
}

func (p sysfsPath) hasUSBDeviceIdentifiers() bool {
	return exists(filepath.Join(string(p), "idVendor")) && exists(filepath.Join(string(p), "idProduct"))
}

// usbAncestor uses the kernel's documented USB sysfs node names to find
// the nearest physical device. Interface nodes contain a colon (for example,
// 1-13:1.0); device nodes contain a bus-port chain such as 1-13 or 1-2.4.
// Keeping this ancestry walk lexical avoids probing the filesystem at every
// level of the SCSI/block-device path.
func (p sysfsPath) usbAncestor() sysfsPath {
	for path := filepath.Clean(string(p)); path != filepath.Dir(path); path = filepath.Dir(path) {
		if isUSBDeviceNode(filepath.Base(path)) {
			return sysfsPath(path)
		}
	}
	return ""
}

func isUSBDeviceNode(name string) bool {
	bus, ports, found := strings.Cut(name, "-")
	if !found {
		return false
	}
	if !decimalDigits(bus) {
		return false
	}
	return isUSBPortChain(ports)
}

func isUSBPortChain(ports string) bool {
	for _, port := range strings.Split(ports, ".") {
		if !decimalDigits(port) {
			return false
		}
	}
	return true
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
