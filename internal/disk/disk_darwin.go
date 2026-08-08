//go:build darwin

// macOS is intentionally a compile-safe outline while the Linux manager is
// brought to production readiness. The future implementation will keep this
// API and use Disk Arbitration/IOKit behind this build-tag boundary.
package disk

import "context"

type darwinManager struct{}

func NewManager() Manager { return &darwinManager{} }

func (*darwinManager) List(context.Context) ([]Disk, error) {
	return nil, ErrUnsupported
}

func (*darwinManager) Refresh(context.Context, string) (Disk, error) {
	return Disk{}, ErrUnsupported
}

func (*darwinManager) Unmount(context.Context, Disk) error {
	return ErrUnsupported
}

func (*darwinManager) Eject(context.Context, Disk) error {
	return ErrUnsupported
}
