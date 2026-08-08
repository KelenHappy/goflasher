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
	Bus         string
	Size        uint64
	Removable   bool
	External    bool
	System      bool
	Mounted     bool
	MountPoints []string
}

// SameIdentity compares the fields that must survive a destructive-operation
// revalidation. Callers do not need to understand platform identity schemes.
func SameIdentity(a, b Disk) bool {
	if a.ID == "" || a.ID != b.ID || a.Device == "" || a.Device != b.Device || a.Size == 0 || a.Size != b.Size {
		return false
	}
	return (a.Serial == "" && b.Serial == "") || (a.Serial != "" && a.Serial == b.Serial)
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
