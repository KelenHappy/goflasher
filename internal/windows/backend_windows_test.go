//go:build windows

package windows

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/progress"
)

type fakeLocks struct{ closes int }

func (f *fakeLocks) Close() error { f.closes++; return nil }

type fakeFile struct {
	bytes.Buffer
	flushes, closes int
}

func (f *fakeFile) Close() error { f.closes++; return nil }
func (f *fakeFile) Flush() error { f.flushes++; return nil }

type fakeAPI struct {
	records          []diskRecord
	locks            *fakeLocks
	file             *fakeFile
	opens, lockCalls int
}

func (f *fakeAPI) list(context.Context) ([]diskRecord, error) {
	return append([]diskRecord(nil), f.records...), nil
}
func (f *fakeAPI) lockVolumes(context.Context, uint32) (volumeLocks, error) {
	f.lockCalls++
	f.locks = &fakeLocks{}
	return f.locks, nil
}
func (f *fakeAPI) openDisk(_ context.Context, _ diskRecord, _ bool) (nativeFile, error) {
	f.opens++
	if f.file == nil {
		f.file = &fakeFile{}
	}
	return f.file, nil
}
func (*fakeAPI) eject(context.Context, diskRecord) error { return nil }
func (*fakeAPI) formatFAT32(context.Context, diskRecord, string, chan<- progress.Update) error {
	return nil
}

func candidate() diskRecord {
	return diskRecord{Device: device.Device{ID: "storage:SERIAL", Path: `\\.\PhysicalDrive4`, Model: "Flash", Serial: "SERIAL", WWN: "storage:SERIAL", Transport: "usb", Major: 4, Size: 16 << 30}, deviceNumber: 4, usbAncestor: true, deviceHotplug: true}
}

func TestPolicyRequiresCorroboratingSignals(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*diskRecord)
		allowed bool
	}{
		{"safe", func(*diskRecord) {}, true},
		{"bus alone", func(r *diskRecord) { r.deviceHotplug = false; r.mediaHotplug = false }, false},
		{"hotplug alone", func(r *diskRecord) { r.usbAncestor = false; r.Transport = "sata" }, false},
		{"no identity", func(r *diskRecord) { r.ID = ""; r.Serial = ""; r.WWN = "" }, false},
		{"system", func(r *diskRecord) { r.IsSystemDisk = true }, false},
		{"virtual", func(r *diskRecord) { r.Transport = "virtual" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := candidate()
			tt.mutate(&r)
			classify(&r)
			if r.IsAllowed != tt.allowed {
				t.Fatalf("allowed=%v", r.IsAllowed)
			}
		})
	}
}
func TestEveryOpenRevalidates(t *testing.T) {
	api := &fakeAPI{records: []diskRecord{candidate()}}
	b := &Backend{api: api, locks: map[string]volumeLocks{}}
	ds, err := b.ListAllowedDevices(context.Background())
	if err != nil || len(ds) != 1 {
		t.Fatal(ds, err)
	}
	if _, err = b.OpenWriter(context.Background(), ds[0]); !errors.Is(err, ErrUnmountFailed) {
		t.Fatal(err)
	}
	if err = b.Unmount(context.Background(), ds[0]); err != nil {
		t.Fatal(err)
	}
	w, err := b.OpenWriter(context.Background(), ds[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if api.opens != 1 || api.lockCalls != 1 || api.locks.closes != 1 {
		t.Fatalf("opens=%d locks=%d closes=%d", api.opens, api.lockCalls, api.locks.closes)
	}
}
func TestChangedIdentityFailsClosed(t *testing.T) {
	r := candidate()
	b := &Backend{api: &fakeAPI{records: []diskRecord{r}}, locks: map[string]volumeLocks{}}
	selected := r.Device
	selected.Size--
	if _, err := b.OpenReader(context.Background(), selected); !errors.Is(err, ErrDeviceChanged) {
		t.Fatalf("%v", err)
	}
}
func TestManagedFileCloseIsIdempotentAndFlushes(t *testing.T) {
	f := &fakeFile{}
	releases := 0
	m := &managedFile{nativeFile: f, release: func() error { releases++; return nil }}
	if _, err := io.Copy(m, bytes.NewBufferString("x")); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	_ = m.Close()
	if f.flushes != 1 || f.closes != 1 || releases != 1 {
		t.Fatalf("flush=%d close=%d release=%d", f.flushes, f.closes, releases)
	}
}

func TestFormatLocksAndRevalidates(t *testing.T) {
	api := &fakeAPI{records: []diskRecord{candidate()}}
	b := &Backend{api: api, locks: map[string]volumeLocks{}}
	d := candidate().Device
	if err := b.FormatFAT32(context.Background(), d, "GOFLASHER", nil); err != nil {
		t.Fatal(err)
	}
	if api.lockCalls != 1 || api.locks.closes != 1 {
		t.Fatalf("lock calls=%d closes=%d", api.lockCalls, api.locks.closes)
	}
}
