//go:build darwin

package disk

import (
	"context"
	"errors"
	"testing"

	darwinapi "github.com/goflasher/goflasher/internal/disk/darwin"
)

type fakeDarwinProbe struct {
	probes               []darwinapi.ProbeResult
	unmountErr, ejectErr error
}

func (f *fakeDarwinProbe) List(context.Context) ([]darwinapi.ProbeResult, error) {
	return append([]darwinapi.ProbeResult(nil), f.probes...), nil
}
func (f *fakeDarwinProbe) Unmount(context.Context, string) error {
	if f.unmountErr == nil {
		f.probes[0].MountPoints = nil
	}
	return f.unmountErr
}
func (f *fakeDarwinProbe) Eject(context.Context, string) error {
	if f.ejectErr == nil {
		f.probes = nil
	}
	return f.ejectErr
}
func safeProbe() darwinapi.ProbeResult {
	return darwinapi.ProbeResult{BSDName: "disk7", MediaName: "media", Size: 1024, Whole: true, Ejectable: true, Removable: true, RegistryID: "abc", RegistryPath: "IOService:/port/media", MediaID: "media-id", TransportSerial: "reader-serial", USBAncestor: true, MountPoints: []string{"/Volumes/X"}}
}

func TestDarwinManagerNativeLifecycle(t *testing.T) {
	f := &fakeDarwinProbe{probes: []darwinapi.ProbeResult{safeProbe()}}
	m := &darwinManager{probe: f}
	disks, err := m.List(context.Background())
	if err != nil || len(disks) != 1 {
		t.Fatalf("List=%+v,%v", disks, err)
	}
	selected := disks[0]
	if err := m.Unmount(context.Background(), selected); err != nil {
		t.Fatal(err)
	}
	selected.Mounted = false
	selected.MountPoints = nil
	if err := m.Eject(context.Background(), selected); err != nil {
		t.Fatal(err)
	}
}
func TestDarwinManagerRejectsIdentityReplacement(t *testing.T) {
	f := &fakeDarwinProbe{probes: []darwinapi.ProbeResult{safeProbe()}}
	m := &darwinManager{probe: f}
	selected, _ := probeDisk(f.probes[0])
	f.probes[0].RegistryID = "replacement"
	if err := m.Unmount(context.Background(), selected); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error=%v", err)
	}
}
func TestDarwinProbeFailsClosed(t *testing.T) {
	p := safeProbe()
	p.USBAncestor = false
	if _, ok := probeDisk(p); ok {
		t.Fatal("non-USB identity was accepted")
	}
	p = safeProbe()
	p.RegistryID = ""
	if _, ok := probeDisk(p); ok {
		t.Fatal("missing operation identity was accepted")
	}
}
