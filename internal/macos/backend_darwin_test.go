//go:build darwin

package macos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/goflasher/goflasher/internal/disk"
)

type fakeManager struct {
	current          disk.Disk
	unmounts, ejects int
}

func TestRawOpenBindsFDToAuthorizedDarwinDisk(t *testing.T) {
	for _, tc := range []struct {
		name       string
		openedRdev int32
		mutate     func(*fakeManager)
		ok         bool
	}{
		{name: "normal", openedRdev: 7, ok: true},
		{name: "rdisk node reused", openedRdev: 8},
		{name: "registry identity changed", openedRdev: 7, mutate: func(m *fakeManager) { m.current.RegistryID = "replacement" }},
		{name: "selected disk disappeared", openedRdev: 7, mutate: func(m *fakeManager) { m.current = disk.Disk{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &fakeManager{current: safeDisk()}
			m.current.Mounted = false
			m.current.MountPoints = nil
			selected := deviceFromDisk(m.current)
			b := NewBackendWithManager(m)
			path := filepath.Join(t.TempDir(), "raw")
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			b.openRaw = func(string, int) (*os.File, error) {
				if tc.mutate != nil {
					tc.mutate(m)
				}
				return os.OpenFile(path, os.O_RDWR, 0)
			}
			b.fstat = func(_ int, st *syscall.Stat_t) error {
				st.Mode, st.Rdev = syscall.S_IFCHR, tc.openedRdev
				return nil
			}
			b.stat = func(_ string, st *syscall.Stat_t) error {
				st.Mode, st.Rdev = syscall.S_IFCHR, 7
				return nil
			}
			f, err := b.OpenWriter(context.Background(), selected)
			if f != nil {
				_ = f.Close()
			}
			if (err == nil) != tc.ok {
				t.Fatalf("open error = %v, want success %t", err, tc.ok)
			}
		})
	}
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

func TestInstallerSessionsReuseIdentityBoundRawOpen(t *testing.T) {
	m := &fakeManager{current: safeDisk()}
	m.current.Mounted, m.current.MountPoints = false, nil
	b := NewBackendWithManager(m)
	selected := deviceFromDisk(m.current)
	path := filepath.Join(t.TempDir(), "raw")
	if err := os.WriteFile(path, make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}
	var flags []int
	b.openRaw = func(openedPath string, flag int) (*os.File, error) {
		if openedPath != m.current.Device {
			t.Fatalf("opened %q, want refreshed %q", openedPath, m.current.Device)
		}
		flags = append(flags, flag)
		return os.OpenFile(path, flag&syscall.O_ACCMODE, 0)
	}
	b.fstat = func(_ int, st *syscall.Stat_t) error { st.Mode, st.Rdev = syscall.S_IFCHR, 7; return nil }
	b.stat = func(_ string, st *syscall.Stat_t) error { st.Mode, st.Rdev = syscall.S_IFCHR, 7; return nil }

	target, err := b.OpenInstallerTarget(context.Background(), selected)
	if err != nil {
		t.Fatal(err)
	}
	_ = target.Close()
	reader, err := b.OpenInstallerReader(context.Background(), selected)
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if len(flags) != 2 || flags[0]&syscall.O_ACCMODE != os.O_RDWR || flags[1]&syscall.O_ACCMODE != os.O_RDONLY {
		t.Fatalf("open flags=%#v", flags)
	}
}
