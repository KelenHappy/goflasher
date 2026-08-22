//go:build windows

package windows

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goflasher/goflasher/internal/device"
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
		{"interior space", "ABC 123", "", "windows:serial=ABC 123", true},
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

// The descriptor is laid out by hand with the ntddstor.h offsets so the parser
// and the storageIdentifier helper cannot drift together.
func TestParseStorageDeviceWWN(t *testing.T) {
	b := make([]byte, 12+16+8)
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)))
	binary.LittleEndian.PutUint32(b[8:12], 1)
	binary.LittleEndian.PutUint32(b[12:16], 1) // CodeSet: binary
	binary.LittleEndian.PutUint32(b[16:20], 3) // Type: FCPH name (NAA)
	binary.LittleEndian.PutUint16(b[20:22], 8) // IdentifierSize
	binary.LittleEndian.PutUint16(b[22:24], 0) // NextOffset
	binary.LittleEndian.PutUint32(b[24:28], 0) // Association: device
	copy(b[28:], []byte{0x50, 0x00, 0xc5, 0x00, 0x12, 0x34, 0x56, 0x78})
	got, err := parseStorageDeviceIDs(b)
	if err != nil || got != "5000C50012345678" {
		t.Fatalf("WWN=%q error=%v", got, err)
	}
	binary.LittleEndian.PutUint32(b[8:12], 2)
	if _, err := parseStorageDeviceIDs(b); err == nil {
		t.Fatal("incomplete identifier list accepted")
	}
}

func TestParseStorageDeviceIDs(t *testing.T) {
	binaryWWN := storageIdentifier(1, 3, 0, []byte{0x50, 0x00, 0xc5, 0x00, 0x12, 0x34, 0x56, 0x78})
	asciiWWN := storageIdentifier(2, 2, 0, []byte(" 5000c50012345678 \x00"))
	utf8WWN := storageIdentifier(3, 2, 0, []byte("5000c50012345678"))
	vendorSpecific := storageIdentifier(1, 0, 0, []byte{0xde, 0xad})
	ignored := storageIdentifier(1, 3, 1, []byte{0xde, 0xad})

	tests := []struct {
		name    string
		data    []byte
		want    string
		wantErr bool
	}{
		{name: "binary NAA", data: storageDeviceIDDescriptor(binaryWWN), want: "5000C50012345678"},
		{name: "normalized ASCII EUI", data: storageDeviceIDDescriptor(asciiWWN), want: "5000C50012345678"},
		{name: "UTF-8 EUI", data: storageDeviceIDDescriptor(utf8WWN), want: "5000C50012345678"},
		{name: "ignores vendor-specific type", data: storageDeviceIDDescriptor(vendorSpecific, binaryWWN), want: "5000C50012345678"},
		{name: "no stable identifier", data: storageDeviceIDDescriptor(vendorSpecific, ignored), want: ""},
		{name: "ignores non-device association", data: storageDeviceIDDescriptor(ignored, binaryWWN), want: "5000C50012345678"},
		{name: "rejects conflicting identifiers", data: storageDeviceIDDescriptor(binaryWWN, storageIdentifier(1, 3, 0, []byte{0x60, 0, 0, 0, 0, 0, 0, 1})), wantErr: true},
		{name: "rejects short descriptor", data: make([]byte, 11), wantErr: true},
		{name: "rejects undersized descriptor size", data: descriptorWithSize(11, 0, nil), wantErr: true},
		{name: "rejects oversized descriptor size", data: descriptorWithSize(64, 0, nil), wantErr: true},
		{name: "rejects missing declared entry", data: descriptorWithSize(12, 1, nil), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStorageDeviceIDs(tt.data)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("WWN=%q error=%v, want %q error=%t", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestParseStorageIdentifierRejectsInvalidEntryBounds(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "truncated header", data: make([]byte, storageIdentifierHeaderSize-1)},
		{name: "empty identifier", data: storageIdentifier(1, 3, 0, nil)},
		{name: "truncated identifier", data: func() []byte {
			entry := storageIdentifier(1, 3, 0, []byte{1})
			binary.LittleEndian.PutUint16(entry[8:10], 2)
			return entry
		}()},
		{name: "next overlaps identifier", data: func() []byte {
			entry := storageIdentifier(1, 3, 0, []byte{1, 2})
			binary.LittleEndian.PutUint16(entry[10:12], storageIdentifierHeaderSize+1)
			return entry
		}()},
		{name: "next exceeds buffer", data: func() []byte {
			entry := storageIdentifier(1, 3, 0, []byte{1})
			binary.LittleEndian.PutUint16(entry[10:12], uint16(len(entry)+1))
			return entry
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := parseStorageIdentifier(tt.data, 0, 4); err == nil {
				t.Fatal("invalid STORAGE_IDENTIFIER accepted")
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

	volume := `\\?\Volume{` + guid + `}\`
	if got := volumeGUID(volume); got != strings.TrimSuffix(volume, `\`) {
		t.Fatalf("volumeGUID(%q) = %q", volume, got)
	}
	if got := volumeGUID(`\\?\Volume{not-a-guid}\`); got != "<unknown-volume>" {
		t.Fatalf("invalid volume GUID exposed as %q", got)
	}
}

// storageIdentifier builds one STORAGE_IDENTIFIER with the ntddstor.h layout:
// 4-byte CodeSet and Type enums, USHORT IdentifierSize and NextOffset, 4-byte
// Association enum, then the identifier bytes.
func storageIdentifier(codeSet, identifierType, association uint32, value []byte) []byte {
	entry := make([]byte, storageIdentifierHeaderSize+len(value))
	binary.LittleEndian.PutUint32(entry[0:4], codeSet)
	binary.LittleEndian.PutUint32(entry[4:8], identifierType)
	binary.LittleEndian.PutUint16(entry[8:10], uint16(len(value)))
	binary.LittleEndian.PutUint32(entry[12:16], association)
	copy(entry[storageIdentifierHeaderSize:], value)
	return entry
}

func storageDeviceIDDescriptor(entries ...[]byte) []byte {
	size := 12
	for _, entry := range entries {
		size += len(entry)
	}
	descriptor := descriptorWithSize(uint32(size), uint32(len(entries)), nil)
	for i, entry := range entries {
		entry = append([]byte(nil), entry...)
		if i+1 < len(entries) {
			binary.LittleEndian.PutUint16(entry[10:12], uint16(len(entry)))
		} else {
			binary.LittleEndian.PutUint16(entry[10:12], 0)
		}
		descriptor = append(descriptor, entry...)
	}
	return descriptor
}

func descriptorWithSize(size, count uint32, payload []byte) []byte {
	descriptor := make([]byte, 12, 12+len(payload))
	binary.LittleEndian.PutUint32(descriptor[4:8], size)
	binary.LittleEndian.PutUint32(descriptor[8:12], count)
	return append(descriptor, payload...)
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

func TestQueryStorageDescriptorTwoPhase(t *testing.T) {
	var sizes []int
	b, err := queryStorageDescriptor(func(out []byte) (uint32, error) {
		sizes = append(sizes, len(out))
		binary.LittleEndian.PutUint32(out[4:8], 40)
		if len(out) < 40 {
			return storageDescriptorHeaderSize, nil
		}
		return 40, nil
	})
	if err != nil || len(b) != 40 || len(sizes) != 2 || sizes[0] != storageDescriptorHeaderSize || sizes[1] != 40 {
		t.Fatalf("len=%d sizes=%v error=%v", len(b), sizes, err)
	}
}

func TestQueryStorageDescriptorGrowsOnBufferError(t *testing.T) {
	calls := 0
	b, err := queryStorageDescriptor(func(out []byte) (uint32, error) {
		calls++
		if len(out) < 512 {
			return 0, windows.ERROR_INSUFFICIENT_BUFFER
		}
		return 100, nil
	})
	if err != nil || len(b) != 100 || calls < 3 {
		t.Fatalf("len=%d calls=%d error=%v", len(b), calls, err)
	}
}

func TestQueryStorageDescriptorRejectsBadHeader(t *testing.T) {
	for _, size := range []uint32{0, 7, maxStorageDescriptorSize + 1} {
		_, err := queryStorageDescriptor(func(out []byte) (uint32, error) {
			binary.LittleEndian.PutUint32(out[4:8], size)
			return storageDescriptorHeaderSize, nil
		})
		if err == nil {
			t.Fatalf("descriptor size %d accepted", size)
		}
	}
}

func TestStoragePropertyQueryLayout(t *testing.T) {
	q := storagePropertyQuery(storageDeviceIDProperty)
	if len(q) != storagePropertyQuerySize || binary.LittleEndian.Uint32(q[0:4]) != storageDeviceIDProperty || binary.LittleEndian.Uint32(q[4:8]) != propertyStandardQuery {
		t.Fatalf("query=%v", q)
	}
}

func TestParseHotplugInfo(t *testing.T) {
	hot, err := parseHotplugInfo([]byte{8, 0, 0, 0, 1, 0, 1, 0})
	if err != nil || hot.media || !hot.device {
		t.Fatalf("flags=%+v error=%v", hot, err)
	}
	hot, err = parseHotplugInfo([]byte{8, 0, 0, 0, 0, 1, 0, 0})
	if err != nil || !hot.media || hot.device {
		t.Fatalf("flags=%+v error=%v", hot, err)
	}
	if _, err := parseHotplugInfo(make([]byte, 7)); err == nil {
		t.Fatal("short STORAGE_HOTPLUG_INFO accepted")
	}
}

func TestDiskRecordFromDescriptor(t *testing.T) {
	const vendor, model, serial = "Vendor", "Model X", " SN 42 "
	q := make([]byte, storageDeviceDescriptorSize)
	off := func(s string) uint32 {
		o := uint32(len(q))
		q = append(q, []byte(s)...)
		q = append(q, 0)
		return o
	}
	vendorOff, modelOff, serialOff := off(vendor), off(model), off(serial)
	binary.LittleEndian.PutUint32(q[12:16], vendorOff)
	binary.LittleEndian.PutUint32(q[16:20], modelOff)
	binary.LittleEndian.PutUint32(q[24:28], serialOff)
	binary.LittleEndian.PutUint32(q[28:32], busTypeUSB)
	r, err := diskRecordFromDescriptor(3, 1<<30, q, "5000c50012345678", hotplugFlags{device: true})
	if err != nil {
		t.Fatal(err)
	}
	want := diskRecord{
		Device:        device.Device{ID: "windows:serial=SN 42;wwn=5000C50012345678", Path: `\\.\PhysicalDrive3`, Vendor: vendor, Model: model, Serial: "SN 42", WWN: "5000C50012345678", Transport: "usb", Major: 3, Size: 1 << 30},
		identity:      windowsIdentityEvidence{Serial: "SN 42", WWN: "5000C50012345678"},
		deviceHotplug: true,
		usbAncestor:   true,
		deviceNumber:  3,
	}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("record=%+v\nwant  =%+v", r, want)
	}
	if _, err := diskRecordFromDescriptor(3, 1, q[:35], "", hotplugFlags{}); err == nil {
		t.Fatal("short STORAGE_DEVICE_DESCRIPTOR accepted")
	}
}

func TestDiskVolumeIndexDedupesExtentsOnSameDisk(t *testing.T) {
	oldEnumerate, oldQuery := enumerateVolumes, queryVolumeDisks
	t.Cleanup(func() { enumerateVolumes, queryVolumeDisks = oldEnumerate, oldQuery })
	enumerateVolumes = func() ([]string, error) { return []string{`\\?\Volume{a}\`, `\\?\Volume{b}\`, `\\?\Volume{a}\`}, nil }
	queries := 0
	queryVolumeDisks = func(v string) ([]uint32, error) {
		queries++
		if strings.Contains(v, "a") {
			return []uint32{3, 3, 5}, nil
		}
		return []uint32{5}, nil
	}
	index, err := diskVolumeIndex()
	if err != nil || queries != 2 {
		t.Fatalf("queries=%d error=%v", queries, err)
	}
	if got := index[3]; len(got) != 1 || got[0] != `\\?\Volume{a}\` {
		t.Fatalf("disk 3 volumes=%v", got)
	}
	if got := index[5]; len(got) != 2 {
		t.Fatalf("disk 5 volumes=%v", got)
	}
	if got := index[9]; len(got) != 0 {
		t.Fatalf("disk 9 volumes=%v", got)
	}
}

func stubLock(t *testing.T, attempts *int, errs ...error) {
	oldOpen, oldLock, oldDelay := openVolumeHandle, lockAndDismount, lockRetryDelay
	t.Cleanup(func() { openVolumeHandle, lockAndDismount, lockRetryDelay = oldOpen, oldLock, oldDelay })
	openVolumeHandle = func(string, uint32) (windows.Handle, error) { return windows.InvalidHandle, nil }
	lockRetryDelay = 0
	lockAndDismount = func(windows.Handle) error {
		*attempts++
		if *attempts <= len(errs) {
			return errs[*attempts-1]
		}
		return nil
	}
}

func TestLockVolumeRetriesTransientDenial(t *testing.T) {
	attempts := 0
	stubLock(t, &attempts, windows.ERROR_ACCESS_DENIED, windows.ERROR_SHARING_VIOLATION)
	if _, err := lockVolume(context.Background(), `\\?\Volume{a}\`); err != nil || attempts != 3 {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
}

func TestLockVolumeGivesUpAfterBoundedAttempts(t *testing.T) {
	attempts := 0
	denials := make([]error, lockVolumeAttempts+2)
	for i := range denials {
		denials[i] = windows.ERROR_ACCESS_DENIED
	}
	stubLock(t, &attempts, denials...)
	_, err := lockVolume(context.Background(), `\\?\Volume{a}\`)
	if !errors.Is(err, ErrVolumeLockDenied) || !errors.Is(err, windows.ERROR_ACCESS_DENIED) || attempts != lockVolumeAttempts {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
}

func TestLockVolumeDoesNotRetryPermanentFailure(t *testing.T) {
	attempts := 0
	stubLock(t, &attempts, windows.ERROR_NOT_SUPPORTED)
	_, err := lockVolume(context.Background(), `\\?\Volume{a}\`)
	if !errors.Is(err, windows.ERROR_NOT_SUPPORTED) || attempts != 1 {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
}

func TestLockVolumeRetryHonorsCancellation(t *testing.T) {
	attempts := 0
	ctx, cancel := context.WithCancel(context.Background())
	oldOpen, oldLock, oldDelay := openVolumeHandle, lockAndDismount, lockRetryDelay
	t.Cleanup(func() { openVolumeHandle, lockAndDismount, lockRetryDelay = oldOpen, oldLock, oldDelay })
	openVolumeHandle = func(string, uint32) (windows.Handle, error) { return windows.InvalidHandle, nil }
	lockRetryDelay = time.Hour
	lockAndDismount = func(windows.Handle) error {
		attempts++
		cancel()
		return windows.ERROR_ACCESS_DENIED
	}
	_, err := lockVolume(ctx, `\\?\Volume{a}\`)
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
}

func TestPnPEnumerationFailureFailsClosed(t *testing.T) {
	oldSystem, oldPnP := querySystemDisks, enumeratePnPDisks
	t.Cleanup(func() { querySystemDisks, enumeratePnPDisks = oldSystem, oldPnP })
	querySystemDisks = func() (map[uint32]bool, error) { return map[uint32]bool{0: true}, nil }
	sentinel := errors.New("setupapi failed")
	enumeratePnPDisks = func() (map[uint32]pnpDisk, error) { return map[uint32]pnpDisk{1: {}}, sentinel }
	_, err := (&winAPI{}).list(context.Background())
	if !errors.Is(err, sentinel) || !errors.Is(err, ErrSystemTopologyUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

func TestListFailsClosedWhenVolumeTopologyUnavailable(t *testing.T) {
	oldSystem, oldPnP, oldEnumerate := querySystemDisks, enumeratePnPDisks, enumerateVolumes
	t.Cleanup(func() { querySystemDisks, enumeratePnPDisks, enumerateVolumes = oldSystem, oldPnP, oldEnumerate })
	querySystemDisks = func() (map[uint32]bool, error) { return map[uint32]bool{}, nil }
	enumeratePnPDisks = func() (map[uint32]pnpDisk, error) { return map[uint32]pnpDisk{}, nil }
	sentinel := errors.New("FindFirstVolumeW failed")
	enumerateVolumes = func() ([]string, error) { return nil, sentinel }
	_, err := (&winAPI{}).list(context.Background())
	if !errors.Is(err, sentinel) || !errors.Is(err, ErrVolumeTopologyUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

func TestHandleLocksCloseIsIdempotent(t *testing.T) {
	l := &handleLocks{}
	l.add(windows.InvalidHandle)
	_ = l.Close()
	if l.handles != nil {
		t.Fatal("handles retained after Close")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close error=%v", err)
	}
}
