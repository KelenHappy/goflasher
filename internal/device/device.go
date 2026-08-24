package device

import (
	"context"
	"io"

	"github.com/goflasher/goflasher/internal/progress"
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
	FormatFAT32(context.Context, Device, string, chan<- progress.Update) error
}

// InstallerTarget is a bounded random-access raw session. Backends must bind
// it to the selected device identity and revalidate safety for every command.
type InstallerTarget interface {
	io.WriterAt
	Sync() error
	Close() error
}
type InstallerReader interface {
	io.ReaderAt
	Close() error
}
type WindowsInstallerBackend interface {
	OpenInstallerTarget(context.Context, Device) (InstallerTarget, error)
	OpenInstallerReader(context.Context, Device) (InstallerReader, error)
}

// SameIdentity compares immutable kernel and hardware identifiers. A serial or
// WWN mismatch is always fatal; major/minor and sysfs path protect devices that
// do not expose either hardware identifier.
func SameIdentity(a, b Device) bool {
	if !sameKernelIdentity(a, b) {
		return false
	}
	if !sameHardwareIdentity(a, b) {
		return false
	}
	if hasHardwareIdentity(a) {
		return true
	}
	return sameSysfsIdentity(a, b)
}

type kernelIdentity struct {
	id, path     string
	major, minor uint32
}

func sameKernelIdentity(a, b Device) bool {
	if a.ID == "" {
		return false
	}
	return kernelIdentityOf(a) == kernelIdentityOf(b)
}

func kernelIdentityOf(d Device) kernelIdentity {
	return kernelIdentity{id: d.ID, path: d.Path, major: d.Major, minor: d.Minor}
}

func sameHardwareIdentity(a, b Device) bool {
	return a.Serial == b.Serial && a.WWN == b.WWN
}

func hasHardwareIdentity(d Device) bool {
	return d.Serial != "" || d.WWN != ""
}

func sameSysfsIdentity(a, b Device) bool {
	return a.SysfsPath != "" && a.SysfsPath == b.SysfsPath
}
