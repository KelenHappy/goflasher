//go:build windows

package windows

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSystemDiskNumbersRejectsInvalidSystemRoot(t *testing.T) {
	for _, root := range []string{"", "C", `C:Windows`, `\\server\Windows`, `1:\Windows`} {
		t.Run(root, func(t *testing.T) {
			t.Setenv("SystemRoot", root)
			disks, err := systemDiskNumbers()
			if err == nil || disks != nil || !strings.Contains(err.Error(), "invalid SystemRoot") {
				t.Fatalf("disks=%v error=%v", disks, err)
			}
		})
	}
}

func TestParseVolumeDiskExtents(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want []uint32
		err  bool
	}{
		{"single extent", extentBuffer(1, 4), []uint32{4}, false},
		{"multiple extents", extentBuffer(2, 4, 7), []uint32{4, 7}, false},
		{"zero extents", extentBuffer(0), []uint32{}, false},
		{"truncated header", make([]byte, 7), nil, true},
		{"truncated entry", extentBuffer(2, 9), nil, true},
		{"huge count", extentBuffer(^uint32(0)), nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVolumeDiskExtents(tt.data)
			if (err != nil) != tt.err || !equalDiskNumbers(got, tt.want) {
				t.Fatalf("got=%v error=%v, want=%v error=%v", got, err, tt.want, tt.err)
			}
		})
	}
}

func TestWindowsIdentityEvidence(t *testing.T) {
	tests := []struct {
		name, serial, wwn, id string
		valid                 bool
	}{
		{"serial only", " abc ", "", "windows:serial=ABC", true},
		{"WWN only", "", "5000c50012345678", "windows:wwn=5000C50012345678", true},
		{"serial and WWN", "abc", "5000c50012345678", "windows:serial=ABC;wwn=5000C50012345678", true},
		{"neither", "", "", "", true},
		{"duplicate evidence", "same", "SAME", "", false},
		{"control character", "bad\nserial", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := newWindowsIdentity(tt.serial, tt.wwn)
			if (err == nil) != tt.valid || (err == nil && e.canonicalID() != tt.id) {
				t.Fatalf("evidence=%+v id=%q error=%v", e, e.canonicalID(), err)
			}
		})
	}
}

func TestParseStorageDeviceWWN(t *testing.T) {
	b := make([]byte, 12+8+8)
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)))
	binary.LittleEndian.PutUint32(b[8:12], 1)
	b[12], b[13], b[18] = 1, 3, 0 // binary, NAA, associated with device
	binary.LittleEndian.PutUint16(b[14:16], 8)
	copy(b[20:], []byte{0x50, 0x00, 0xc5, 0x00, 0x12, 0x34, 0x56, 0x78})
	got, err := parseStorageDeviceIDs(b)
	if err != nil || got != "5000C50012345678" {
		t.Fatalf("WWN=%q error=%v", got, err)
	}
	binary.LittleEndian.PutUint32(b[8:12], 2)
	if _, err := parseStorageDeviceIDs(b); err == nil {
		t.Fatal("incomplete identifier list accepted")
	}
}

func TestIdentityAdmissionRequiresSerialOrWWN(t *testing.T) {
	for _, evidence := range []windowsIdentityEvidence{{Serial: "SERIAL"}, {WWN: "5000C50012345678"}, {Serial: "SERIAL", WWN: "5000C50012345678"}} {
		r := candidate()
		r.identity = evidence
		r.ID, r.Serial, r.WWN = evidence.canonicalID(), evidence.Serial, evidence.WWN
		classify(&r)
		if !r.IsAllowed {
			t.Fatalf("evidence %+v rejected: %s", evidence, r.RejectReason)
		}
	}
	r := candidate()
	r.identity, r.ID, r.Serial, r.WWN = windowsIdentityEvidence{}, "", "", ""
	classify(&r)
	if r.IsAllowed || r.RejectReason != "no trustworthy persistent identity" {
		t.Fatalf("allowed=%v reason=%q", r.IsAllowed, r.RejectReason)
	}
}

func TestQueryVolumeExtentsGrowsBuffer(t *testing.T) {
	calls := 0
	b, err := queryVolumeExtents(func(out []byte) (uint32, error) {
		calls++
		if len(out) < 1024 {
			return 0, windows.ERROR_MORE_DATA
		}
		copy(out, extentBuffer(1, 8))
		return 32, nil
	})
	if err != nil || calls != 3 || len(b) != 32 {
		t.Fatalf("len=%d calls=%d error=%v", len(b), calls, err)
	}
}

func TestQueryVolumeExtentsGrowthIsBounded(t *testing.T) {
	calls := 0
	_, err := queryVolumeExtents(func([]byte) (uint32, error) {
		calls++
		return 0, windows.ERROR_INSUFFICIENT_BUFFER
	})
	if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || calls == 0 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestVolumesForDiskFailsOnPartialEnumeration(t *testing.T) {
	oldEnumerate, oldQuery := enumerateVolumes, queryVolumeDisks
	t.Cleanup(func() { enumerateVolumes, queryVolumeDisks = oldEnumerate, oldQuery })
	enumerateVolumes = func() ([]string, error) { return []string{`\\?\Volume{one}\`, `\\?\Volume{two}\`}, nil }
	sentinel := errors.New("extent query failed")
	queryVolumeDisks = func(v string) ([]uint32, error) {
		if strings.Contains(v, "two") {
			return nil, sentinel
		}
		return []uint32{3}, nil
	}
	if got, err := volumesForDisk(3); got != nil || !errors.Is(err, sentinel) {
		t.Fatalf("volumes=%v error=%v", got, err)
	}
}

func TestVolumesForDiskZeroVolumeSuccess(t *testing.T) {
	old := enumerateVolumes
	t.Cleanup(func() { enumerateVolumes = old })
	enumerateVolumes = func() ([]string, error) { return nil, nil }
	got, err := volumesForDisk(3)
	if err != nil || len(got) != 0 {
		t.Fatalf("volumes=%v error=%v", got, err)
	}
}

func TestTopologyComparisonDetectsRace(t *testing.T) {
	if !sameVolumes([]string{"a", "b"}, []string{"b", "a"}) {
		t.Fatal("same topology rejected because order changed")
	}
	for _, current := range [][]string{{"a"}, {"a", "b", "c"}, {"a", "c"}} {
		if sameVolumes([]string{"a", "b"}, current) {
			t.Fatalf("topology race accepted: %v", current)
		}
	}
}

func TestVolumeAccessDeniedPreservesSentinelAndHidesPath(t *testing.T) {
	old := openVolumeHandle
	t.Cleanup(func() { openVolumeHandle = old })
	openVolumeHandle = func(string, uint32) (windows.Handle, error) {
		return windows.InvalidHandle, windows.ERROR_ACCESS_DENIED
	}
	_, err := volumeDisks(`C:\Users\private`)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || strings.Contains(err.Error(), "Users") || !strings.Contains(err.Error(), "volume <unknown-volume> open") {
		t.Fatalf("error=%v", err)
	}
}

func TestVolumeExtentQueryErrorNamesGUIDAndOperation(t *testing.T) {
	oldOpen, oldCall := openVolumeHandle, callVolumeExtentIOCTL
	t.Cleanup(func() { openVolumeHandle, callVolumeExtentIOCTL = oldOpen, oldCall })
	openVolumeHandle = func(string, uint32) (windows.Handle, error) { return windows.InvalidHandle, nil }
	callVolumeExtentIOCTL = func(windows.Handle, []byte) (uint32, error) { return 0, windows.ERROR_ACCESS_DENIED }
	guid := `\\?\Volume{12345678-1234-1234-1234-123456789abc}\`
	_, err := volumeDisks(guid)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || !strings.Contains(err.Error(), strings.TrimSuffix(guid, `\`)) || !strings.Contains(err.Error(), "IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS") {
		t.Fatalf("error=%v", err)
	}
}

func TestNativeOperationsHonorPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &winAPI{}
	if _, err := a.list(ctx); !errors.Is(err, context.Canceled) {
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
	querySystemDisks = func() (map[uint32]bool, error) {
		return nil, fmt.Errorf("system volume IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS: %w", sentinel)
	}
	_, err := (&winAPI{}).list(context.Background())
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "identify Windows system disk") || !strings.Contains(err.Error(), "IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS") {
		t.Fatalf("error=%v", err)
	}
}

func equalDiskNumbers(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func extentBuffer(count uint32, disks ...uint32) []byte {
	b := make([]byte, 8+24*len(disks))
	binary.LittleEndian.PutUint32(b[:4], count)
	for i, disk := range disks {
		binary.LittleEndian.PutUint32(b[8+i*24:], disk)
	}
	return b
}
