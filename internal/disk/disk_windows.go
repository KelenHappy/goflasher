//go:build windows

// Windows is intentionally a compile-safe outline while the Linux manager is
// brought to production readiness. Keep the common API intact so the native
// Win32 implementation can be added without changing callers.
package disk

import "context"

type windowsManager struct{}

func NewManager() Manager { return &windowsManager{} }

func (*windowsManager) List(context.Context) ([]Disk, error) {
	return nil, ErrUnsupported
}

func (*windowsManager) Refresh(context.Context, string) (Disk, error) {
	return Disk{}, ErrUnsupported
}

func (*windowsManager) Unmount(context.Context, Disk) error {
	return ErrUnsupported
}

func (*windowsManager) Eject(context.Context, Disk) error {
	return ErrUnsupported
}
