//go:build darwin

package disk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	darwinapi "github.com/goflasher/goflasher/internal/disk/darwin"
	"github.com/goflasher/goflasher/internal/disk/darwin/native"
)

const darwinOperationTimeout = 2 * time.Minute

type darwinProbe interface {
	List(context.Context) ([]darwinapi.ProbeResult, error)
	Unmount(context.Context, string) error
	Eject(context.Context, string) error
}

type darwinManager struct {
	probe   darwinProbe
	openErr error
}

func NewManager() Manager {
	p, err := darwinapi.OpenNativeAdapter()
	return &darwinManager{probe: p, openErr: err}
}

func probeDisk(p darwinapi.ProbeResult) (Disk, bool) {
	// A registry entry ID is deliberately scoped to this enumeration lifetime.
	// Registry path, BSD node, size and model never substitute for it.
	if !eligibleDarwinProbe(p) {
		return Disk{}, false
	}
	mounts := append([]string(nil), p.MountPoints...)
	return Disk{
		ID: "darwin-registry:" + p.RegistryID, Device: "/dev/r" + p.BSDName,
		Vendor: p.Vendor, Model: firstNonempty(p.Product, p.MediaName),
		Bus: "usb", Size: p.Size, Removable: true, External: true, Ejectable: true,
		Mounted: len(mounts) != 0, MountPoints: mounts,
		RegistryID: p.RegistryID, RegistryPath: p.RegistryPath, MediaID: p.MediaID, TransportSerial: p.TransportSerial,
	}, true
}

func eligibleDarwinProbe(p darwinapi.ProbeResult) bool {
	if !p.Whole || p.Internal {
		return false
	}
	if !p.Removable || !p.Ejectable {
		return false
	}
	if !p.USBAncestor || p.Size == 0 {
		return false
	}
	return p.RegistryID != "" && p.BSDName != ""
}

func firstNonempty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (m *darwinManager) List(ctx context.Context) ([]Disk, error) {
	if m.openErr != nil || m.probe == nil {
		return nil, fmt.Errorf("open native Darwin disk manager: %w", errors.Join(ErrUnsupported, m.openErr))
	}
	probes, err := m.probe.List(ctx)
	if err != nil {
		return nil, mapDarwinError(err)
	}
	out := make([]Disk, 0, len(probes))
	for _, p := range probes {
		if d, ok := probeDisk(p); ok {
			out = append(out, d)
		}
	}
	return out, nil
}

func (m *darwinManager) Refresh(ctx context.Context, id string) (Disk, error) {
	all, err := m.List(ctx)
	if err != nil {
		return Disk{}, err
	}
	for _, d := range all {
		if d.ID == id {
			return d, nil
		}
	}
	return Disk{}, ErrNotFound
}

func validateDarwinSelection(selected, fresh Disk) error {
	if isSystemSelection(selected, fresh) {
		return ErrSystemDisk
	}
	if !isRemovableUSB(selected) || !isRemovableUSB(fresh) {
		return ErrNotRemovable
	}
	if !SameIdentity(selected, fresh) {
		return ErrChanged
	}
	return nil
}

func isSystemSelection(selected, fresh Disk) bool {
	return selected.System || fresh.System
}

func isRemovableUSB(d Disk) bool {
	if !d.Removable || !d.External {
		return false
	}
	return d.Ejectable && d.Bus == "usb"
}

func bsdName(device string) (string, bool) {
	name := strings.TrimPrefix(device, "/dev/r")
	return name, name != device && strings.HasPrefix(name, "disk") && !strings.Contains(name, "/")
}

func (m *darwinManager) Unmount(ctx context.Context, selected Disk) error {
	ctx, cancel := context.WithTimeout(ctx, darwinOperationTimeout)
	defer cancel()
	fresh, err := m.Refresh(ctx, selected.ID)
	if err != nil {
		return err
	}
	if err := validateDarwinSelection(selected, fresh); err != nil {
		return err
	}
	bsd, ok := bsdName(fresh.Device)
	if !ok {
		return ErrChanged
	}
	if err := m.probe.Unmount(ctx, bsd); err != nil {
		return errors.Join(ErrUnmountFailed, mapDarwinError(err))
	}
	again, err := m.Refresh(ctx, selected.ID)
	if err != nil {
		return errors.Join(ErrUnmountFailed, err)
	}
	if err := validateDarwinSelection(fresh, again); err != nil || again.Mounted {
		return errors.Join(ErrUnmountFailed, err)
	}
	return nil
}

func (m *darwinManager) Eject(ctx context.Context, selected Disk) error {
	ctx, cancel := context.WithTimeout(ctx, darwinOperationTimeout)
	defer cancel()
	bsd, err := m.ejectTarget(ctx, selected)
	if err != nil {
		return err
	}
	if err := m.probe.Eject(ctx, bsd); err != nil {
		return errors.Join(ErrEjectFailed, mapDarwinError(err))
	}
	return m.waitForEjection(ctx, selected.ID)
}

func (m *darwinManager) ejectTarget(ctx context.Context, selected Disk) (string, error) {
	fresh, err := m.Refresh(ctx, selected.ID)
	if err != nil {
		return "", err
	}
	if err := validateDarwinSelection(selected, fresh); err != nil {
		return "", err
	}
	bsd, ok := bsdName(fresh.Device)
	if !ok {
		return "", ErrChanged
	}
	return bsd, nil
}

func (m *darwinManager) waitForEjection(ctx context.Context, id string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := m.Refresh(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return errors.Join(ErrEjectFailed, err)
		}
		select {
		case <-ctx.Done():
			return errors.Join(ErrEjectFailed, ctx.Err())
		case <-ticker.C:
		}
	}
}

func mapDarwinError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var dissenter native.DissenterError
	if errors.As(err, &dissenter) {
		return fmt.Errorf("%w: %v", ErrPermission, err)
	}
	if errors.Is(err, native.ErrUnavailable) {
		return ErrNotFound
	}
	return err
}
