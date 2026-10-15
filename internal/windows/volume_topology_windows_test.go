//go:build windows

package windows

import (
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
			requireErrorContains(t, err, "invalid SystemRoot")
			if disks != nil {
				t.Fatalf("disks=%v returned alongside error", disks)
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

func TestVolumeGUIDValidation(t *testing.T) {
	const guid = "01234567-89ab-CDEF-0123-456789abcdef"
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "canonical mixed case", value: guid, valid: true},
		{name: "too short", value: guid[:35]},
		{name: "separator in wrong position", value: "0123456-789ab-CDEF-0123-456789abcdef"},
		{name: "missing separator", value: "0123456789ab-CDEF-0123-456789abcdef"},
		{name: "non hexadecimal byte", value: "01234567-89ab-CDEG-0123-456789abcdef"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isGUID(tt.value); got != tt.valid {
				t.Fatalf("isGUID(%q) = %t, want %t", tt.value, got, tt.valid)
			}
		})
	}

	volume := "\\\\?\\Volume{" + guid + "}\\"
	if got := volumeGUID(volume); got != strings.TrimSuffix(volume, "\\") {
		t.Fatalf("volumeGUID(%q) = %q", volume, got)
	}
	if got := volumeGUID("\\\\?\\Volume{not-a-guid}\\"); got != "<unknown-volume>" {
		t.Fatalf("invalid volume GUID exposed as %q", got)
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
	requireNoError(t, err)
	requireEqual(t, "calls", calls, 3)
	requireEqual(t, "len", len(b), 32)
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

func TestVolumeAccessDeniedPreservesSentinelAndHidesPath(t *testing.T) {
	old := openVolumeHandle
	t.Cleanup(func() { openVolumeHandle = old })
	openVolumeHandle = func(string, uint32) (windows.Handle, error) {
		return windows.InvalidHandle, windows.ERROR_ACCESS_DENIED
	}
	_, err := volumeDisks(`C:\Users\private`)
	requireErrorIs(t, err, windows.ERROR_ACCESS_DENIED)
	requireErrorContains(t, err, "volume <unknown-volume> open")
	if strings.Contains(err.Error(), "Users") {
		t.Fatalf("error exposes path: %v", err)
	}
}

func TestVolumeExtentQueryErrorNamesGUIDAndOperation(t *testing.T) {
	oldOpen, oldCall := openVolumeHandle, callVolumeExtentIOCTL
	t.Cleanup(func() { openVolumeHandle, callVolumeExtentIOCTL = oldOpen, oldCall })
	openVolumeHandle = func(string, uint32) (windows.Handle, error) { return windows.InvalidHandle, nil }
	callVolumeExtentIOCTL = func(windows.Handle, []byte) (uint32, error) {
		return 0, windows.ERROR_ACCESS_DENIED
	}
	guid := `\\?\Volume{12345678-1234-1234-1234-123456789abc}\`
	_, err := volumeDisks(guid)
	requireErrorIs(t, err, windows.ERROR_ACCESS_DENIED)
	requireErrorContains(t, err, strings.TrimSuffix(guid, "\\"))
	requireErrorContains(t, err, "IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS")
}

func TestVolumesForDiskFailsOnPartialEnumeration(t *testing.T) {
	const volOne, volTwo = `\\?\Volume{one}\`, `\\?\Volume{two}\`
	oldEnumerate, oldQuery := enumerateVolumes, queryVolumeDisks
	t.Cleanup(func() { enumerateVolumes, queryVolumeDisks = oldEnumerate, oldQuery })
	enumerateVolumes = func() ([]string, error) { return []string{volOne, volTwo}, nil }
	sentinel := errors.New("extent query failed")
	queryVolumeDisks = func(v string) ([]uint32, error) {
		if v == volTwo {
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

func TestDiskVolumeIndexDedupesExtentsOnSameDisk(t *testing.T) {
	const volA, volB = `\\?\Volume{a}\`, `\\?\Volume{b}\`
	old := enumerateVolumes
	t.Cleanup(func() { enumerateVolumes = old })
	enumerateVolumes = func() ([]string, error) { return []string{volA, volB, volA}, nil }

	queries := 0
	index, err := diskVolumeIndexUsing(func(v string) ([]uint32, error) {
		queries++
		if v == volA {
			return []uint32{3, 3, 5}, nil // two extents of volA land on disk 3
		}
		return []uint32{5}, nil
	})

	requireNoError(t, err)
	requireEqual(t, "queries", queries, 2)                       // volA enumerated twice, queried once
	requireEqual(t, "disk 3", strings.Join(index[3], " "), volA) // duplicate extents collapse
	requireEqual(t, "disk 5", strings.Join(index[5], " "), volA+" "+volB)
	requireEqual(t, "disk 9", len(index[9]), 0)
}

// Optical drives, mounted ISO images, and empty card-reader slots have no
// disk extents; they must not hide every USB disk from the list.
func TestDiskVolumeIndexSkipsVolumesWithoutDiskExtents(t *testing.T) {
	const cdrom, empty, usb = `\\?\Volume{c}\`, `\\?\Volume{e}\`, `\\?\Volume{u}\`
	old := enumerateVolumes
	t.Cleanup(func() { enumerateVolumes = old })
	enumerateVolumes = func() ([]string, error) { return []string{cdrom, empty, usb}, nil }

	index, err := diskVolumeIndexUsing(func(v string) ([]uint32, error) {
		switch v {
		case cdrom:
			return nil, fmt.Errorf("volume IOCTL: %w", windows.ERROR_INVALID_FUNCTION)
		case empty:
			return nil, fmt.Errorf("volume open: %w", windows.ERROR_NOT_READY)
		}
		return []uint32{2}, nil
	})

	requireNoError(t, err)
	requireEqual(t, "disk 2", strings.Join(index[2], " "), usb)
}

func TestDiskVolumeIndexFailsClosedOnOtherVolumeErrors(t *testing.T) {
	old := enumerateVolumes
	t.Cleanup(func() { enumerateVolumes = old })
	enumerateVolumes = func() ([]string, error) { return []string{`\\?\Volume{a}\`}, nil }
	_, err := diskVolumeIndexUsing(func(string) ([]uint32, error) { return nil, windows.ERROR_ACCESS_DENIED })
	if !errors.Is(err, ErrVolumeTopologyUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

// TestDiskVolumeIndexUsesGlobalQuery covers the wiring the dedupe test skips:
// diskVolumeIndex must feed diskVolumeIndexUsing with the package-level
// queryVolumeDisks hook rather than a query of its own.
func TestDiskVolumeIndexUsesGlobalQuery(t *testing.T) {
	const vol = `\\?\Volume{a}\`
	oldEnumerate, oldQuery := enumerateVolumes, queryVolumeDisks
	t.Cleanup(func() { enumerateVolumes, queryVolumeDisks = oldEnumerate, oldQuery })
	enumerateVolumes = func() ([]string, error) { return []string{vol}, nil }
	queried := ""
	queryVolumeDisks = func(v string) ([]uint32, error) { queried = v; return []uint32{7}, nil }

	index, err := diskVolumeIndex()
	requireNoError(t, err)
	requireEqual(t, "queried volume", queried, vol)
	requireEqual(t, "disk 7", strings.Join(index[7], " "), vol)
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
