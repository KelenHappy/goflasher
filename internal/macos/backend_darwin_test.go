//go:build darwin

package macos

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/goflasher/goflasher/internal/disk"
)

type fakeManager struct {
	current          disk.Disk
	unmounts, ejects int
}

func (f *fakeManager) List(context.Context) ([]disk.Disk, error) {
	if f.current.ID == "" {
		return nil, nil
	}
	return []disk.Disk{f.current}, nil
}
func (f *fakeManager) Refresh(_ context.Context, id string) (disk.Disk, error) {
	if id != f.current.ID || id == "" {
		return disk.Disk{}, disk.ErrNotFound
	}
	return f.current, nil
}
func (f *fakeManager) Unmount(context.Context, disk.Disk) error {
	f.unmounts++
	f.current.Mounted = false
	f.current.MountPoints = nil
	return nil
}
func (f *fakeManager) Eject(context.Context, disk.Disk) error {
	f.ejects++
	f.current = disk.Disk{}
	return nil
}
func safeDisk() disk.Disk {
	return disk.Disk{ID: "darwin-registry:abc", Device: "/dev/rdisk7", Vendor: "V", Model: "M", Serial: "S", Bus: "usb", Size: 1024, Removable: true, External: true, Ejectable: true, Mounted: true, MountPoints: []string{"/Volumes/X"}, RegistryID: "abc", RegistryPath: "IOService:/port/media"}
}
func TestBackendDelegatesPolicyToManager(t *testing.T) {
	m := &fakeManager{current: safeDisk()}
	b := NewBackendWithManager(m)
	list, err := b.ListAllowedDevices(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	if err = b.Unmount(context.Background(), list[0]); err != nil || m.unmounts != 1 {
		t.Fatalf("unmount=%v count=%d", err, m.unmounts)
	}
	list[0].Mounted = false
	list[0].MountPoints = nil
	if err = b.Eject(context.Background(), list[0]); err != nil || m.ejects != 1 {
		t.Fatalf("eject=%v count=%d", err, m.ejects)
	}
}
func TestBackendRejectsDeviceNodeReuse(t *testing.T) {
	m := &fakeManager{current: safeDisk()}
	b := NewBackendWithManager(m)
	selected := deviceFromDisk(m.current)
	m.current.RegistryID = "replacement"
	m.current.ID = "darwin-registry:replacement"
	if _, err := b.diskSnapshot(context.Background(), selected); !errors.Is(err, ErrDeviceChanged) {
		t.Fatalf("error=%v", err)
	}
}
func TestOpenNeverUsesSelectedPath(t *testing.T) {
	m := &fakeManager{current: safeDisk()}
	m.current.Mounted = false
	b := NewBackendWithManager(m)
	selected := deviceFromDisk(m.current)
	selected.Path = "/dev/rdisk999"
	called := ""
	b.openRaw = func(path string, flag int) (*os.File, error) { called = path; return nil, os.ErrPermission }
	_, _ = b.OpenWriter(context.Background(), selected)
	if called != "" {
		t.Fatalf("selected path reached raw opener: %q", called)
	}
}
