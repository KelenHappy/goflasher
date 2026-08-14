//go:build windows

package disk

import (
	"context"
	"io"
	"testing"

	"github.com/goflasher/goflasher/internal/device"
)

func TestPhysicalNumber(t *testing.T) {
	tests := []struct {
		path string
		want uint32
		ok   bool
	}{
		{`\\.\PhysicalDrive0`, 0, true},
		{`\\.\PhysicalDrive42`, 42, true},
		{`\\.\PhysicalDrive4294967295`, ^uint32(0), true},
		{`\\.\PhysicalDrive`, 0, false},
		{`PhysicalDrive1`, 0, false},
		{`\\.\physicaldrive1`, 0, false},
		{`\\.\PhysicalDrive-1`, 0, false},
		{`\\.\PhysicalDrive1x`, 0, false},
		{`\\.\PhysicalDrive4294967296`, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := physicalNumber(tt.path)
			if (err == nil) != tt.ok || got != tt.want {
				t.Fatalf("physicalNumber(%q) = %d, %v; want %d, ok=%v", tt.path, got, err, tt.want, tt.ok)
			}
		})
	}
}

func TestWindowsAdapterRoundTripPreservesIdentityEvidence(t *testing.T) {
	for _, d := range []device.Device{
		{ID: "windows:serial=SERIAL", Path: `\\.\PhysicalDrive7`, Serial: "SERIAL", Major: 7},
		{ID: "windows:wwn=5000C50012345678", Path: `\\.\PhysicalDrive7`, WWN: "5000C50012345678", Major: 7},
		{ID: "windows:serial=SERIAL;wwn=5000C50012345678", Path: `\\.\PhysicalDrive7`, Serial: "SERIAL", WWN: "5000C50012345678", Major: 7},
	} {
		roundTrip, err := toDevice(fromDevice(d))
		if err != nil {
			t.Fatal(err)
		}
		if !device.SameIdentity(d, roundTrip) {
			t.Fatalf("round trip = %+v, want %+v", roundTrip, d)
		}
	}
}

type locatorBackend struct{ called bool }

func (*locatorBackend) ListAllowedDevices(context.Context) ([]device.Device, error) { return nil, nil }
func (*locatorBackend) RefreshDevice(context.Context, string) (device.Device, error) {
	return device.Device{}, nil
}
func (b *locatorBackend) Unmount(context.Context, device.Device) error { b.called = true; return nil }
func (*locatorBackend) OpenWriter(context.Context, device.Device) (io.WriteCloser, error) {
	panic("unused")
}
func (*locatorBackend) OpenReader(context.Context, device.Device) (io.ReadCloser, error) {
	panic("unused")
}
func (*locatorBackend) Flush(context.Context, device.Device) error   { panic("unused") }
func (b *locatorBackend) Eject(context.Context, device.Device) error { b.called = true; return nil }

func TestMalformedLocatorDoesNotReachBackend(t *testing.T) {
	b := &locatorBackend{}
	m := &windowsManager{backend: b}
	if err := m.Unmount(context.Background(), Disk{Device: "bad"}); err == nil || b.called {
		t.Fatalf("error=%v backend called=%v", err, b.called)
	}
}
