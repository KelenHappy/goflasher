//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"golang.org/x/sys/windows"
)

func TestNativeOperationsHonorPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &winAPI{}
	if _, _, err := a.list(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("list error=%v", err)
	}
	if _, err := a.lockVolumes(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("lock error=%v", err)
	}
	if _, err := a.openDisk(ctx, diskRecord{}, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("open error=%v", err)
	}
}

func TestSystemDiskExtentQueryFailureIsWrapped(t *testing.T) {
	old := querySystemDisks
	t.Cleanup(func() { querySystemDisks = old })
	sentinel := windows.ERROR_ACCESS_DENIED
	querySystemDisks = func() (map[diskNumber]bool, error) {
		return nil, fmt.Errorf("system volume IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS: %w", sentinel)
	}
	api := &winAPI{}
	_, _, err := api.list(context.Background())
	requireErrorIs(t, err, sentinel)
	requireErrorContains(t, err, "identify Windows system disk")
	requireErrorContains(t, err, "IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS")
}

func TestPnPEnumerationFailureFailsClosed(t *testing.T) {
	oldSystem, oldPnP := querySystemDisks, enumeratePnPDisks
	t.Cleanup(func() { querySystemDisks, enumeratePnPDisks = oldSystem, oldPnP })
	querySystemDisks = func() (map[diskNumber]bool, error) { return map[diskNumber]bool{0: true}, nil }
	sentinel := errors.New("setupapi failed")
	enumeratePnPDisks = func() (map[diskNumber]pnpDisk, error) { return map[diskNumber]pnpDisk{1: {}}, sentinel }
	api := &winAPI{}
	_, _, err := api.list(context.Background())
	if !errors.Is(err, sentinel) || !errors.Is(err, ErrSystemTopologyUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

func TestListFailsClosedWhenVolumeTopologyUnavailable(t *testing.T) {
	oldSystem, oldPnP, oldEnumerate := querySystemDisks, enumeratePnPDisks, enumerateVolumes
	t.Cleanup(func() { querySystemDisks, enumeratePnPDisks, enumerateVolumes = oldSystem, oldPnP, oldEnumerate })
	querySystemDisks = func() (map[diskNumber]bool, error) { return map[diskNumber]bool{}, nil }
	enumeratePnPDisks = func() (map[diskNumber]pnpDisk, error) { return map[diskNumber]pnpDisk{}, nil }
	sentinel := errors.New("FindFirstVolumeW failed")
	enumerateVolumes = func() ([]volumePath, error) { return nil, sentinel }
	api := &winAPI{}
	_, _, err := api.list(context.Background())
	if !errors.Is(err, sentinel) || !errors.Is(err, ErrVolumeTopologyUnavailable) {
		t.Fatalf("error=%v", err)
	}
}
