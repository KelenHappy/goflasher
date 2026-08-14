//go:build windows

package disk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/goflasher/goflasher/internal/device"
	win "github.com/goflasher/goflasher/internal/windows"
)

type windowsManager struct{ backend device.Backend }

func NewManager() Manager { return &windowsManager{backend: win.NewBackend()} }
func fromDevice(d device.Device) Disk {
	return Disk{ID: d.ID, Device: d.Path, Vendor: d.Vendor, Model: d.Model, Serial: d.Serial, WWN: d.WWN, Bus: d.Transport, Size: d.Size, Removable: d.IsAllowed, External: d.IsAllowed, System: d.IsSystemDisk, Mounted: d.Mounted, MountPoints: d.MountPoints}
}
func toDevice(d Disk) (device.Device, error) {
	n, err := physicalNumber(d.Device)
	if err != nil {
		return device.Device{}, fmt.Errorf("invalid Windows disk locator: %w", err)
	}
	return device.Device{ID: d.ID, Path: d.Device, Vendor: d.Vendor, Model: d.Model, Serial: d.Serial, WWN: d.WWN, Transport: d.Bus, Major: n, Size: d.Size, IsSystemDisk: d.System, IsAllowed: d.External && d.Removable, Mounted: d.Mounted, MountPoints: d.MountPoints}, nil
}
func physicalNumber(path string) (uint32, error) {
	const prefix = `\\.\PhysicalDrive`
	if !strings.HasPrefix(path, prefix) {
		return 0, fmt.Errorf("expected %q prefix", prefix)
	}
	suffix := path[len(prefix):]
	if suffix == "" {
		return 0, errors.New("physical drive number is empty")
	}
	for _, c := range []byte(suffix) {
		if c < '0' || c > '9' {
			return 0, errors.New("physical drive number must contain only ASCII digits")
		}
	}
	n, err := strconv.ParseUint(suffix, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("physical drive number: %w", err)
	}
	return uint32(n), nil
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
	dev, err := toDevice(d)
	if err != nil {
		return err
	}
	return m.backend.Unmount(ctx, dev)
}
func (m *windowsManager) Eject(ctx context.Context, d Disk) error {
	dev, err := toDevice(d)
	if err != nil {
		return err
	}
	return m.backend.Eject(ctx, dev)
}
