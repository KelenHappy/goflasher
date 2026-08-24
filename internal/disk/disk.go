// Package disk provides one platform-neutral API for discovering and managing
// physical disks. OS handles, ioctl values, Disk Arbitration references, and
// other native details never cross this package boundary.
package disk

import (
	"context"
	"errors"
)

var (
	ErrNotFound      = errors.New("disk not found")
	ErrChanged       = errors.New("disk identity changed")
	ErrPermission    = errors.New("disk operation requires elevated permission")
	ErrUnsupported   = errors.New("disk operation is not supported")
	ErrSystemDisk    = errors.New("refusing to manage a system disk")
	ErrNotRemovable  = errors.New("disk is not external removable media")
	ErrUnmountFailed = errors.New("disk could not be unmounted")
	ErrEjectFailed   = errors.New("disk could not be ejected")
)

// Disk is an immutable snapshot returned by Manager. ID is the stable key used
// by Refresh; Device is informational and must never be trusted as identity.
type Disk struct {
	ID          string
	Device      string
	Vendor      string
	Model       string
	Serial      string
	WWN         string
	Bus         string
	Size        uint64
	Removable   bool
	External    bool
	System      bool
	Mounted     bool
	MountPoints []string
	// RegistryID is an operation-lifetime IOKit identity. RegistryPath is only
	// attachment evidence and must never authorize an operation by itself.
	RegistryID   string
	RegistryPath string
	Ejectable    bool
	MediaID      string
	// TransportSerial identifies a reader/USB transport, not its media.
	TransportSerial string
}

// SameIdentity compares the fields that must survive a destructive-operation
// revalidation. Callers do not need to understand platform identity schemes.
func SameIdentity(a, b Disk) bool {
	if !hasRequiredIdentity(a) {
		return false
	}
	return identityOf(a) == identityOf(b)
}

type diskIdentity struct {
	id, device, registryID, registryPath  string
	mediaID, transportSerial, wwn, serial string
	size                                  uint64
}

func hasRequiredIdentity(d Disk) bool {
	if d.ID == "" {
		return false
	}
	if d.Device == "" {
		return false
	}
	return d.Size != 0
}

func identityOf(d Disk) diskIdentity {
	return diskIdentity{
		id: d.ID, device: d.Device, size: d.Size,
		registryID: d.RegistryID, registryPath: d.RegistryPath,
		mediaID: d.MediaID, transportSerial: d.TransportSerial,
		wwn: d.WWN, serial: d.Serial,
	}
}

// Manager is the complete cross-platform disk-management surface. Each
// operation accepts only common types; native objects remain private to the
// file selected by the platform build tag.
type Manager interface {
	List(context.Context) ([]Disk, error)
	Refresh(context.Context, string) (Disk, error)
	Unmount(context.Context, Disk) error
	Eject(context.Context, Disk) error
}
