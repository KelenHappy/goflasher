package device

import (
	"context"
	"io"
)

// Device is a stable snapshot of a candidate target. Backends must refresh it
// immediately before opening a device and compare Identity fields.
type Device struct {
	ID, Path, Vendor, Model, Serial, WWN, Transport, SysfsPath string
	Major, Minor                                               uint32
	Size                                                       uint64
	IsCardReader, Mounted, IsSystemDisk, IsAllowed             bool
	MountPoints                                                []string
	PartitionCount                                             int
	RejectReason                                               string
}

// Backend isolates privileged, platform-specific device operations.
type Backend interface {
	ListAllowedDevices(context.Context) ([]Device, error)
	RefreshDevice(context.Context, string) (Device, error)
	Unmount(context.Context, Device) error
	OpenWriter(context.Context, Device) (io.WriteCloser, error)
	OpenReader(context.Context, Device) (io.ReadCloser, error)
	Flush(context.Context, Device) error
	Eject(context.Context, Device) error
}

// FAT32Formatter is implemented by platform backends that can destructively
// replace a device's existing layout with a single FAT32 filesystem.
type FAT32Formatter interface {
	FormatFAT32(context.Context, Device, string) error
}

// SameIdentity compares immutable kernel and hardware identifiers. A serial or
// WWN mismatch is always fatal; major/minor and sysfs path protect devices that
// do not expose either hardware identifier.
func SameIdentity(a, b Device) bool {
	if a.ID == "" || b.ID == "" || a.ID != b.ID || a.Path != b.Path {
		return false
	}
	if a.Major != b.Major || a.Minor != b.Minor {
		return false
	}
	if (a.Serial != "" || b.Serial != "") && (a.Serial == "" || a.Serial != b.Serial) {
		return false
	}
	if (a.WWN != "" || b.WWN != "") && (a.WWN == "" || a.WWN != b.WWN) {
		return false
	}
	if a.Serial != "" || a.WWN != "" {
		return true
	}
	return a.SysfsPath != "" && a.SysfsPath == b.SysfsPath
}
