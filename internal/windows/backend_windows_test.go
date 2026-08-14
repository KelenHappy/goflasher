//go:build windows

package windows

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
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
	listErr          error
	lockErr          error
	locks            *fakeLocks
	file             *fakeFile
	opens, lockCalls int
	openWrites       []bool
}

func (f *fakeAPI) list(context.Context) ([]diskRecord, error) {
	return append([]diskRecord(nil), f.records...), f.listErr
}
func (f *fakeAPI) lockVolumes(context.Context, uint32) (volumeLocks, error) {
	f.lockCalls++
	if f.lockErr != nil {
		return nil, f.lockErr
	}
	f.locks = &fakeLocks{}
	return f.locks, nil
}
func (f *fakeAPI) openDisk(_ context.Context, _ diskRecord, write bool) (nativeFile, error) {
	f.opens++
	f.openWrites = append(f.openWrites, write)
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
	e := windowsIdentityEvidence{Serial: "SERIAL"}
	return diskRecord{Device: device.Device{ID: e.canonicalID(), Path: `\\.\PhysicalDrive4`, Model: "Flash", Serial: e.Serial, Transport: "usb", Major: 4, Size: 16 << 30}, identity: e, deviceNumber: 4, usbAncestor: true, deviceHotplug: true}
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

func TestUSBSystemDiskIsRejected(t *testing.T) {
	r := candidate()
	r.IsSystemDisk = true
	classify(&r)
	if r.IsAllowed || r.RejectReason != ErrSystemDisk.Error() {
		t.Fatalf("allowed=%v reason=%q", r.IsAllowed, r.RejectReason)
	}
}

func TestSystemDiskQueryErrorFailsEnumeration(t *testing.T) {
	want := errors.New("system disk query failed")
	b := &Backend{api: &fakeAPI{listErr: want}, locks: map[string]volumeLocks{}}
	_, err := b.ListAllowedDevices(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
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
	if api.opens != 1 || api.lockCalls != 1 || api.locks.closes != 0 {
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

func TestPersistentIdentityChangesFailClosed(t *testing.T) {
	base := candidate().Device
	tests := []struct {
		name   string
		mutate func(*device.Device)
	}{
		{"serial disappears", func(d *device.Device) { d.Serial = "" }},
		{"serial changes", func(d *device.Device) { d.Serial = "OTHER"; d.ID = "windows:serial=OTHER" }},
		{"new WWN appears", func(d *device.Device) { d.WWN = "5000C50012345678"; d.ID += ";wwn=" + d.WWN }},
		{"disk number reused", func(d *device.Device) { d.Serial = "REPLACEMENT"; d.ID = "windows:serial=REPLACEMENT" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := base
			tt.mutate(&current)
			if !deviceChanged(base, current) {
				t.Fatal("identity change was accepted")
			}
		})
	}
}

func TestCandidateWithoutPersistentIdentityIsHiddenWithDiagnostic(t *testing.T) {
	r := candidate()
	r.identity, r.ID, r.Serial, r.WWN = windowsIdentityEvidence{}, "", "", ""
	b := &Backend{api: &fakeAPI{records: []diskRecord{r}}, locks: map[string]volumeLocks{}}
	got, err := b.ListAllowedDevices(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("devices=%v error=%v", got, err)
	}
	rs, err := b.records(context.Background())
	if err != nil || len(rs) != 1 || rs[0].RejectReason != "no trustworthy persistent identity" {
		t.Fatalf("records=%v error=%v", rs, err)
	}
}
func TestManagedFileCloseIsIdempotentAndFlushes(t *testing.T) {
	f := &fakeFile{}
	m := &managedFile{nativeFile: f, flushOnClose: true}
	if _, err := io.Copy(m, bytes.NewBufferString("x")); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	_ = m.Close()
	if f.flushes != 1 || f.closes != 1 {
		t.Fatalf("flush=%d close=%d", f.flushes, f.closes)
	}
}

func TestFlushOpensDiskForWriteAndRetainsLocks(t *testing.T) {
	api := &fakeAPI{records: []diskRecord{candidate()}}
	b := &Backend{api: api, locks: map[string]volumeLocks{}}
	d := candidate().Device
	if err := b.Unmount(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(api.openWrites) != 1 || !api.openWrites[0] {
		t.Fatalf("open write flags = %v, want [true]", api.openWrites)
	}
	if api.locks.closes != 0 {
		t.Fatalf("locks closed during flush = %d", api.locks.closes)
	}
	if err := b.ReleaseDevice(d); err != nil {
		t.Fatal(err)
	}
	if api.locks.closes != 1 {
		t.Fatalf("locks closed after release = %d, want 1", api.locks.closes)
	}
}

func TestRepeatedUnmountCleansUpPreviousLocks(t *testing.T) {
	api := &fakeAPI{records: []diskRecord{candidate()}}
	b := &Backend{api: api, locks: map[string]volumeLocks{}}
	d := candidate().Device
	if err := b.Unmount(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	first := api.locks
	if err := b.Unmount(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if first.closes != 1 || api.locks == first || api.locks.closes != 0 {
		t.Fatalf("first closes=%d same lock=%v current closes=%d", first.closes, api.locks == first, api.locks.closes)
	}
	if err := b.ReleaseDevice(d); err != nil || api.locks.closes != 1 {
		t.Fatalf("release error=%v current closes=%d", err, api.locks.closes)
	}
}

func TestIncompleteVolumeEnumerationPreventsRawWriter(t *testing.T) {
	sentinel := errors.New("volume extent query failed")
	api := &fakeAPI{records: []diskRecord{candidate()}, lockErr: sentinel}
	b := &Backend{api: api, locks: map[string]volumeLocks{}}
	d := candidate().Device
	if err := b.Unmount(context.Background(), d); !errors.Is(err, ErrUnmountFailed) || !errors.Is(err, sentinel) || !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatalf("unmount error=%v", err)
	}
	if _, err := b.OpenWriter(context.Background(), d); !errors.Is(err, ErrUnmountFailed) {
		t.Fatalf("writer error=%v", err)
	}
	if api.opens != 0 {
		t.Fatalf("raw opens=%d, want 0", api.opens)
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
