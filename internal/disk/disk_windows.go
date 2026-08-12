//go:build windows

package disk

import (
	"context"
	"errors"
	"os"

	"github.com/goflasher/goflasher/internal/device"
	win "github.com/goflasher/goflasher/internal/windows"
)

type windowsManager struct{ backend device.Backend }

func NewManager() Manager { return &windowsManager{backend: win.NewBackend()} }
func fromDevice(d device.Device) Disk {
	return Disk{ID: d.ID, Device: d.Path, Vendor: d.Vendor, Model: d.Model, Serial: d.Serial, Bus: d.Transport, Size: d.Size, Removable: d.IsAllowed, External: d.IsAllowed, System: d.IsSystemDisk, Mounted: d.Mounted, MountPoints: d.MountPoints}
}
func toDevice(d Disk) device.Device {
	return device.Device{ID: d.ID, Path: d.Device, Vendor: d.Vendor, Model: d.Model, Serial: d.Serial, WWN: d.ID, Transport: d.Bus, Major: physicalNumber(d.Device), Size: d.Size, IsSystemDisk: d.System, IsAllowed: d.External && d.Removable, Mounted: d.Mounted, MountPoints: d.MountPoints}
}
func physicalNumber(path string) uint32 {
	var n uint32
	for i := len(`\\.\PhysicalDrive`); i < len(path); i++ {
		if path[i] < '0' || path[i] > '9' {
			return 0
		}
		n = n*10 + uint32(path[i]-'0')
	}
	return n
}
func (m *windowsManager) List(ctx context.Context) ([]Disk, error) {
	ds, err := m.backend.ListAllowedDevices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Disk, len(ds))
	for i, d := range ds {
		out[i] = fromDevice(d)
	}
	return out, nil
}
func (m *windowsManager) Refresh(ctx context.Context, id string) (Disk, error) {
	d, err := m.backend.RefreshDevice(ctx, id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Disk{}, ErrNotFound
		}
		return Disk{}, err
	}
	return fromDevice(d), nil
}
func (m *windowsManager) Unmount(ctx context.Context, d Disk) error {
	return m.backend.Unmount(ctx, toDevice(d))
}
func (m *windowsManager) Eject(ctx context.Context, d Disk) error {
	return m.backend.Eject(ctx, toDevice(d))
}
