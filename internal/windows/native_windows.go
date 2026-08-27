//go:build windows

package windows

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/fat32"
	"github.com/goflasher/goflasher/internal/progress"
	"golang.org/x/sys/windows"
)

const (
	ioctlStorageQueryProperty       = 0x002d1400
	ioctlStorageGetDeviceNumber     = 0x002d1080
	ioctlStorageGetHotplugInfo      = 0x002d0c14
	ioctlDiskGetLengthInfo          = 0x0007405c
	ioctlVolumeGetVolumeDiskExtents = 0x00560000
	ioctlStorageMediaRemoval        = 0x002d4804
	ioctlStorageEjectMedia          = 0x002d4808
	fsctlLockVolume                 = 0x00090018
	fsctlDismountVolume             = 0x00090020
	busTypeUSB                      = 7
	storageDeviceProperty           = 0
	storageDeviceIDProperty         = 2
	propertyStandardQuery           = 0

	// sizeof(STORAGE_PROPERTY_QUERY): PropertyId and QueryType enums plus the
	// AdditionalParameters[1] array padded to DWORD alignment.
	storagePropertyQuerySize = 12
	// sizeof(STORAGE_DESCRIPTOR_HEADER): Version and Size.
	storageDescriptorHeaderSize = 8
	// STORAGE_DEVICE_DESCRIPTOR through RawPropertiesLength.
	storageDeviceDescriptorSize = 36
	// STORAGE_DEVICE_ID_DESCRIPTOR header: Version, Size, NumberOfIdentifiers.
	storageDeviceIDDescriptorSize = 12
	// STORAGE_IDENTIFIER header as laid out by ntddstor.h: CodeSet and Type are
	// 4-byte enums, IdentifierSize and NextOffset are USHORTs, Association is a
	// 4-byte enum, and the variable-length Identifier follows at offset 16.
	storageIdentifierHeaderSize = 16
	// STORAGE_HOTPLUG_INFO: Size followed by MediaRemovable, MediaHotplug,
	// DeviceHotplug, and WriteCacheEnableOverride BOOLEAN fields.
	storageHotplugInfoSize   = 8
	maxStorageDescriptorSize = 1 << 20

	storageIDCodeSetBinary = 1
	storageIDCodeSetASCII  = 2
	storageIDCodeSetUTF8   = 3
	storageIDTypeEUI64     = 2
	storageIDTypeFCPHName  = 3
	storageIDAssocDevice   = 0
)

type winAPI struct{}

func newWinAPI() nativeAPI { return &winAPI{} }

func openHandle(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
}

func ioctl(h windows.Handle, code uint32, out []byte) error {
	var p *byte
	if len(out) > 0 {
		p = &out[0]
	}
	var n uint32
	return windows.DeviceIoControl(h, code, nil, 0, p, uint32(len(out)), &n, nil)
}

func query(h windows.Handle, code uint32, size int) ([]byte, error) {
	b := make([]byte, size)
	var n uint32
	if err := windows.DeviceIoControl(h, code, nil, 0, &b[0], uint32(len(b)), &n, nil); err != nil {
		return nil, err
	}
	return b[:n], nil
}

func descriptorString(b []byte, off uint32) string {
	if off == 0 || int(off) >= len(b) {
		return ""
	}
	return strings.TrimSpace(cString(b[off:]))
}

func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

var busNames = map[uint32]string{1: "scsi", 3: "ata", 7: "usb", 8: "raid", 11: "sata", 17: "nvme", 14: "virtual", 15: "filebackedvirtual"}

func busName(v uint32) string { return busNames[v] }

func normalizeIdentity(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// validIdentityComponent is intentionally small and extensible. Placeholder
// blacklists belong here only after hardware evidence demonstrates them.
// Interior spaces are allowed because some vendors embed them in serials;
// normalizeIdentity already strips the surrounding ones.
func validIdentityComponent(s string) bool {
	if s == "" || len(s) > 512 {
		return false
	}
	for _, c := range s {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func newWindowsIdentity(serial, wwn string) (windowsIdentityEvidence, error) {
	e := windowsIdentityEvidence{Serial: normalizeIdentity(serial), WWN: normalizeIdentity(wwn)}
	if e.Serial != "" && !validIdentityComponent(e.Serial) {
		return windowsIdentityEvidence{}, errors.New("invalid storage serial identity")
	}
	if e.WWN != "" && !validIdentityComponent(e.WWN) {
		return windowsIdentityEvidence{}, errors.New("invalid storage WWN identity")
	}
	if e.Serial != "" && e.Serial == e.WWN {
		return windowsIdentityEvidence{}, errors.New("duplicate serial and WWN identity evidence")
	}
	return e, nil
}

func (e windowsIdentityEvidence) canonicalID() string {
	switch {
	case e.Serial != "" && e.WWN != "":
		return "windows:serial=" + e.Serial + ";wwn=" + e.WWN
	case e.Serial != "":
		return "windows:serial=" + e.Serial
	case e.WWN != "":
		return "windows:wwn=" + e.WWN
	default:
		return ""
	}
}

func sameWindowsIdentity(a, b windowsIdentityEvidence) bool {
	return a == b
}

// Conflicting WWNs are rejected because choosing one based on descriptor order
// would make the device identity unstable.
func parseStorageDeviceIDs(b []byte) (string, error) {
	descriptor, count, err := parseStorageDeviceIDDescriptor(b)
	if err != nil {
		return "", err
	}
	return parseStorageIdentifiers(descriptor, count)
}

func parseStorageIdentifiers(descriptor []byte, count uint32) (string, error) {
	off := storageDeviceIDDescriptorSize
	var found string
	for i := uint32(0); i < count; i++ {
		value, next, err := parseStorageIdentifier(descriptor, off, i)
		if err != nil {
			return "", err
		}
		found, err = mergeStorageIdentifier(found, value)
		if err != nil {
			return "", err
		}
		if err := validateStorageIdentifierLink(next, i+1, count); err != nil {
			return "", err
		}
		if next != 0 {
			off += next
		}
	}
	return found, nil
}

func mergeStorageIdentifier(found, value string) (string, error) {
	if identifiersConflict(found, value) {
		return "", errors.New("conflicting WWN identifiers")
	}
	if value != "" {
		return value, nil
	}
	return found, nil
}

func validateStorageIdentifierLink(next int, parsed, count uint32) error {
	if parsed < count && next == 0 {
		return errors.New("incomplete STORAGE_IDENTIFIER list")
	}
	if parsed == count && next != 0 {
		return errors.New("STORAGE_IDENTIFIER list exceeds declared count")
	}
	return nil
}

func parseStorageDeviceIDDescriptor(b []byte) ([]byte, uint32, error) {
	if len(b) < storageDeviceIDDescriptorSize {
		return nil, 0, fmt.Errorf("short STORAGE_DEVICE_ID_DESCRIPTOR: got %d bytes", len(b))
	}
	size := binary.LittleEndian.Uint32(b[4:8])
	if size < storageDeviceIDDescriptorSize || uint64(size) > uint64(len(b)) {
		return nil, 0, fmt.Errorf("invalid STORAGE_DEVICE_ID_DESCRIPTOR size %d for %d bytes", size, len(b))
	}
	return b[:size], binary.LittleEndian.Uint32(b[8:12]), nil
}

func identifiersConflict(found, value string) bool {
	return found != "" && value != "" && found != value
}

// Only device-associated NAA and EUI identifiers provide stable WWN evidence;
// other valid identifier types are intentionally ignored.
func parseStorageIdentifier(b []byte, off int, index uint32) (string, int, error) {
	if off+storageIdentifierHeaderSize > len(b) {
		return "", 0, fmt.Errorf("truncated STORAGE_IDENTIFIER header %d", index)
	}
	codeSet := binary.LittleEndian.Uint32(b[off : off+4])
	idType := binary.LittleEndian.Uint32(b[off+4 : off+8])
	idSize := int(binary.LittleEndian.Uint16(b[off+8 : off+10]))
	next := int(binary.LittleEndian.Uint16(b[off+10 : off+12]))
	association := binary.LittleEndian.Uint32(b[off+12 : off+16])
	if !identifierFits(b, off, idSize) {
		return "", 0, fmt.Errorf("truncated STORAGE_IDENTIFIER entry %d", index)
	}
	if !validIdentifierNextOffset(b, off, idSize, next) {
		return "", 0, fmt.Errorf("invalid STORAGE_IDENTIFIER next offset %d", next)
	}
	if !stableStorageIdentifier(idType, association) {
		return "", next, nil
	}
	raw := b[off+storageIdentifierHeaderSize : off+storageIdentifierHeaderSize+idSize]
	switch codeSet {
	case storageIDCodeSetBinary:
		return fmt.Sprintf("%X", raw), next, nil
	case storageIDCodeSetASCII, storageIDCodeSetUTF8:
		return normalizeIdentity(cString(raw)), next, nil
	default:
		return "", next, nil
	}
}

func identifierFits(b []byte, off, size int) bool {
	return size > 0 && off+storageIdentifierHeaderSize+size <= len(b)
}

func validIdentifierNextOffset(b []byte, off, size, next int) bool {
	return next == 0 || next >= storageIdentifierHeaderSize+size && off+next <= len(b)
}

func stableStorageIdentifier(idType, association uint32) bool {
	return association == storageIDAssocDevice && (idType == storageIDTypeEUI64 || idType == storageIDTypeFCPHName)
}

// storagePropertyIOCTL issues IOCTL_STORAGE_QUERY_PROPERTY with distinct input
// and output buffers so the driver never overwrites the query it is reading.
var storagePropertyIOCTL = func(h windows.Handle, in, out []byte) (uint32, error) {
	var n uint32
	err := windows.DeviceIoControl(h, ioctlStorageQueryProperty, &in[0], uint32(len(in)), &out[0], uint32(len(out)), &n, nil)
	return n, err
}

func storagePropertyQuery(property uint32) []byte {
	in := make([]byte, storagePropertyQuerySize)
	binary.LittleEndian.PutUint32(in[0:4], property)
	binary.LittleEndian.PutUint32(in[4:8], propertyStandardQuery)
	return in
}

func queryStorageProperty(h windows.Handle, property uint32) ([]byte, error) {
	in := storagePropertyQuery(property)
	return queryStorageDescriptor(func(out []byte) (uint32, error) {
		return storagePropertyIOCTL(h, in, out)
	})
}

// queryStorageDescriptor performs the documented two-phase query: a
// header-sized buffer yields STORAGE_DESCRIPTOR_HEADER.Size, which then sizes
// the full request. Drivers that report the short buffer as an error instead
// of filling the header fall back to geometric growth.
func queryStorageDescriptor(call ioctlCall) ([]byte, error) {
	header := make([]byte, storageDescriptorHeaderSize)
	n, err := call(header)
	if bufferTooSmall(err) {
		return queryGrowingBuffer(maxStorageDescriptorSize, call)
	}
	if err != nil {
		return nil, err
	}
	if n < storageDescriptorHeaderSize {
		return nil, fmt.Errorf("short STORAGE_DESCRIPTOR_HEADER: got %d bytes", n)
	}
	size := binary.LittleEndian.Uint32(header[4:8])
	if size < storageDescriptorHeaderSize || size > maxStorageDescriptorSize {
		return nil, fmt.Errorf("invalid storage descriptor size %d", size)
	}
	out := make([]byte, size)
	if n, err = call(out); err != nil {
		return nil, err
	}
	if n > uint32(len(out)) {
		return nil, fmt.Errorf(
			"invalid storage descriptor byte count %d for %d-byte buffer",
			n, len(out),
		)
	}
	if n < storageDescriptorHeaderSize {
		return nil, fmt.Errorf("short storage descriptor: got %d bytes", n)
	}
	return out[:n], nil
}

func bufferTooSmall(err error) bool {
	return errors.Is(err, windows.ERROR_MORE_DATA) || errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER)
}

func propertyUnsupported(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SUPPORTED) || errors.Is(err, windows.ERROR_INVALID_FUNCTION) || errors.Is(err, windows.ERROR_INVALID_PARAMETER)
}

// A malformed or conflicting identifier descriptor yields no WWN rather than
// hiding the disk: the serial alone is accepted identity evidence, and the
// absence of a WWN is as stable across enumerations as any choice among
// conflicting candidates would be unstable.
func storageWWN(h windows.Handle) (string, error) {
	q, err := queryStorageProperty(h, storageDeviceIDProperty)
	if propertyUnsupported(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("IOCTL_STORAGE_QUERY_PROPERTY StorageDeviceIdProperty: %w", err)
	}
	wwn, err := parseStorageDeviceIDs(q)
	if err != nil {
		return "", nil
	}
	return wwn, nil
}

// diskEvidence is everything inspectHandle learns from a disk handle, kept
// free of the handle itself so record construction is a pure function.
type diskEvidence struct {
	number     uint32
	length     uint64
	descriptor []byte
	wwn        string
	hotplug    hotplugFlags
}

func inspectHandle(h windows.Handle, number uint32) (diskRecord, error) {
	if err := verifyDeviceNumber(h, number); err != nil {
		return diskRecord{}, err
	}
	ev, err := gatherDiskEvidence(h, number)
	if err != nil {
		return diskRecord{}, err
	}
	return diskRecordFromEvidence(ev)
}

func gatherDiskEvidence(h windows.Handle, number uint32) (diskEvidence, error) {
	ev := diskEvidence{number: number}
	var err error
	if ev.length, err = diskLength(h); err != nil {
		return diskEvidence{}, err
	}
	if ev.descriptor, err = storageDescriptor(h); err != nil {
		return diskEvidence{}, err
	}
	if ev.hotplug, err = hotplugInfo(h); err != nil {
		return diskEvidence{}, err
	}
	if ev.wwn, err = storageWWN(h); err != nil {
		return diskEvidence{}, err
	}
	return ev, nil
}

func verifyDeviceNumber(h windows.Handle, expected uint32) error {
	n, err := query(h, ioctlStorageGetDeviceNumber, 12)
	if err != nil {
		return fmt.Errorf("IOCTL_STORAGE_GET_DEVICE_NUMBER: %w", err)
	}
	if len(n) < 8 {
		return errors.New("short STORAGE_DEVICE_NUMBER")
	}
	if binary.LittleEndian.Uint32(n[4:8]) != expected {
		return ErrDeviceChanged
	}
	return nil
}

func diskLength(h windows.Handle) (uint64, error) {
	length, err := query(h, ioctlDiskGetLengthInfo, 8)
	if err != nil {
		return 0, fmt.Errorf("IOCTL_DISK_GET_LENGTH_INFO: %w", err)
	}
	if len(length) < 8 {
		return 0, errors.New("short GET_LENGTH_INFORMATION")
	}
	return binary.LittleEndian.Uint64(length), nil
}

func storageDescriptor(h windows.Handle) ([]byte, error) {
	q, err := queryStorageProperty(h, storageDeviceProperty)
	if err != nil {
		return nil, fmt.Errorf("IOCTL_STORAGE_QUERY_PROPERTY StorageDeviceProperty: %w", err)
	}
	if len(q) < storageDeviceDescriptorSize {
		return nil, fmt.Errorf("short STORAGE_DEVICE_DESCRIPTOR: got %d bytes", len(q))
	}
	return q, nil
}

type hotplugFlags struct{ media, device bool }

// Hotplug evidence gates admission, so a failed query is an error rather than
// a silent "not removable" verdict.
func hotplugInfo(h windows.Handle) (hotplugFlags, error) {
	b, err := query(h, ioctlStorageGetHotplugInfo, storageHotplugInfoSize)
	if err != nil {
		return hotplugFlags{}, fmt.Errorf("IOCTL_STORAGE_GET_HOTPLUG_INFO: %w", err)
	}
	return parseHotplugInfo(b)
}

func parseHotplugInfo(b []byte) (hotplugFlags, error) {
	if len(b) < storageHotplugInfoSize {
		return hotplugFlags{}, fmt.Errorf("short STORAGE_HOTPLUG_INFO: got %d bytes", len(b))
	}
	return hotplugFlags{media: b[5] != 0, device: b[6] != 0}, nil
}

func diskRecordFromEvidence(ev diskEvidence) (diskRecord, error) {
	q := ev.descriptor
	if len(q) < storageDeviceDescriptorSize {
		return diskRecord{}, fmt.Errorf("short STORAGE_DEVICE_DESCRIPTOR: got %d bytes", len(q))
	}
	serial := descriptorString(q, binary.LittleEndian.Uint32(q[24:28]))
	identity, err := newWindowsIdentity(serial, ev.wwn)
	if err != nil {
		return diskRecord{}, err
	}
	vendor := descriptorString(q, binary.LittleEndian.Uint32(q[12:16]))
	model := descriptorString(q, binary.LittleEndian.Uint32(q[16:20]))
	bus := binary.LittleEndian.Uint32(q[28:32])
	// Serial plus the immutable descriptor identity is preferred. Devices that
	// do not expose one fail policy rather than falling back to disk number.
	id := identity.canonicalID()
	path := physicalDrivePath(ev.number)
	r := diskRecord{Device: device.Device{ID: id, Path: path, Vendor: vendor, Model: model, Serial: identity.Serial, WWN: identity.WWN, Transport: busName(bus), Major: ev.number, Size: ev.length}, identity: identity, deviceNumber: ev.number, usbAncestor: bus == busTypeUSB}
	r.mediaHotplug, r.deviceHotplug = ev.hotplug.media, ev.hotplug.device
	return r, nil
}

func physicalDrivePath(number uint32) string {
	return `\\.\PhysicalDrive` + strconv.FormatUint(uint64(number), 10)
}

// Enumeration fails closed when safety-related topology is unavailable rather
// than returning disks whose eligibility could be misclassified. The PnP tree
// is part of that topology: without it USB attachment would rest on bus type
// alone, and the disk set itself comes from the PnP disk-interface class.
func (a *winAPI) list(ctx context.Context) ([]diskRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	systems, err := querySystemDisks()
	if err != nil {
		return nil, fmt.Errorf("%w: identify Windows system disk: %w", ErrSystemTopologyUnavailable, err)
	}
	pnp, err := enumeratePnPDisks()
	if err != nil {
		return nil, fmt.Errorf("%w: enumerate PnP disks: %w", ErrSystemTopologyUnavailable, err)
	}
	volumes, err := diskVolumeIndex()
	if err != nil {
		return nil, err
	}
	return enumerateDiskRecords(ctx, systems, pnp, volumes)
}

func enumerateDiskRecords(ctx context.Context, systems map[uint32]bool, pnp map[uint32]pnpDisk, volumes map[uint32][]string) ([]diskRecord, error) {
	var out []diskRecord
	for _, n := range slices.Sorted(maps.Keys(pnp)) {
		r, found, err := inspectDiskNumber(ctx, n)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		out = append(out, completeDiskRecord(r, systems[n], pnp[n], len(volumes[n]) > 0))
	}
	return out, nil
}

func completeDiskRecord(r diskRecord, system bool, pnp pnpDisk, mounted bool) diskRecord {
	r.IsSystemDisk = system
	r.SysfsPath, r.devInst = pnp.instance, pnp.devInst
	r.usbAncestor = r.usbAncestor || pnp.usb
	r.Mounted = mounted
	return r
}

// A disk that cannot be inspected is omitted rather than listed with partial
// evidence. Expected causes are media-less readers (ERROR_NOT_READY) and
// drivers that reject identity queries; both fail closed by omission.
func inspectDiskNumber(ctx context.Context, number uint32) (diskRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return diskRecord{}, false, err
	}
	h, err := openHandle(physicalDrivePath(number), 0)
	if err != nil {
		return diskRecord{}, false, nil
	}
	defer windows.CloseHandle(h)
	r, err := inspectHandle(h, number)
	return r, err == nil, nil
}

type winFile struct {
	f   *os.File
	ctx context.Context
}

func (f *winFile) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.f.Read(p)
}

func (f *winFile) Write(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.f.Write(p)
}

func (f *winFile) WriteAt(p []byte, off int64) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.f.WriteAt(p, off)
}

func (f *winFile) Close() error { return f.f.Close() }

// Flush and Sync are the same operation: os.File.Sync is FlushFileBuffers on
// Windows. Both names exist to satisfy nativeFile and fat32.Device.
func (f *winFile) Flush() error { return f.f.Sync() }
func (f *winFile) Sync() error  { return f.Flush() }

func (a *winAPI) openDisk(ctx context.Context, r diskRecord, write bool) (nativeFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	access := uint32(windows.GENERIC_READ)
	if write {
		access |= windows.GENERIC_WRITE
	}
	h, err := openHandle(r.Path, access)
	if err != nil {
		return nil, err
	}
	fresh, err := inspectHandle(h, r.deviceNumber)
	if err != nil || diskRecordChanged(r, fresh) {
		windows.CloseHandle(h)
		if err != nil {
			return nil, err
		}
		return nil, ErrDeviceChanged
	}
	return &winFile{f: os.NewFile(uintptr(h), r.Path), ctx: ctx}, nil
}

func diskRecordChanged(selected, fresh diskRecord) bool {
	return !sameWindowsIdentity(fresh.identity, selected.identity) || fresh.ID != selected.ID || fresh.Size != selected.Size
}

type handleLocks struct{ handles []windows.Handle }

func (l *handleLocks) add(h windows.Handle) { l.handles = append(l.handles, h) }

// Close is idempotent; handles are released exactly once.
func (l *handleLocks) Close() error {
	var first error
	for _, h := range l.handles {
		if err := windows.CloseHandle(h); err != nil && first == nil {
			first = err
		}
	}
	l.handles = nil
	return first
}

func (a *winAPI) lockVolumes(ctx context.Context, n uint32) (volumeLocks, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vols, err := volumesForDisk(n)
	if err != nil {
		return nil, err
	}
	locks := &handleLocks{}
	locked := make(map[string]windows.Handle, len(vols))
	for _, v := range vols {
		h, err := lockVolume(ctx, v)
		if err != nil {
			locks.Close()
			return nil, err
		}
		locks.add(h)
		locked[v] = h
	}
	if err := validateLockedVolumes(n, vols, locked); err != nil {
		locks.Close()
		return nil, err
	}
	return locks, nil
}

const (
	lockVolumeAttempts     = 5
	lockVolumeInitialDelay = 100 * time.Millisecond
)

// Lock primitives are variables so the retry policy is testable without a
// real volume handle.
var (
	lockAndDismount = func(h windows.Handle) error {
		if err := ioctl(h, fsctlLockVolume, nil); err != nil {
			return err
		}
		return ioctl(h, fsctlDismountVolume, nil)
	}
	lockRetryDelay = lockVolumeInitialDelay
)

func lockVolume(ctx context.Context, volume string) (windows.Handle, error) {
	if err := ctx.Err(); err != nil {
		return windows.InvalidHandle, err
	}
	h, err := openVolumeHandle(strings.TrimSuffix(volume, `\`), windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("%w: volume %s open: %w", ErrVolumeLockDenied, volumeGUID(volume), err)
	}
	if err := lockWithRetry(ctx, h); err != nil {
		windows.CloseHandle(h)
		return windows.InvalidHandle, fmt.Errorf("%w: volume %s lock/dismount: %w", ErrVolumeLockDenied, volumeGUID(volume), err)
	}
	return h, nil
}

// FSCTL_LOCK_VOLUME fails with ERROR_ACCESS_DENIED while any other handle to
// the volume is open, which is routine right after insertion (shell
// thumbnails, search indexing, antivirus scans). Those handles close within
// moments, so a short exponential retry turns a spurious failure into success
// without masking a genuine denial.
func lockWithRetry(ctx context.Context, h windows.Handle) error {
	delay := lockRetryDelay
	for attempt := 1; ; attempt++ {
		err := lockAndDismount(h)
		if err == nil {
			return nil
		}
		if !shouldRetryLock(err, attempt) {
			return err
		}
		if err := sleepContext(ctx, delay); err != nil {
			return err
		}
		delay *= 2
	}
}

func shouldRetryLock(err error, attempt int) bool {
	return attempt < lockVolumeAttempts && retryableLockError(err)
}

func retryableLockError(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func validateLockedVolumes(number uint32, expected []string, locked map[string]windows.Handle) error {
	current, err := volumesForDiskUsing(number, func(v string) ([]uint32, error) {
		if h, ok := locked[v]; ok {
			return volumeDisksFromHandle(v, h)
		}
		return queryVolumeDisks(v)
	})
	if err != nil {
		return err
	}
	if !sameVolumes(expected, current) {
		return fmt.Errorf("%w: volume topology changed while locking", ErrDeviceChanged)
	}
	return nil
}

func sameVolumes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]int, len(a))
	for _, v := range a {
		set[v]++
	}
	for _, v := range b {
		if set[v] == 0 {
			return false
		}
		set[v]--
	}
	return true
}

func (a *winAPI) eject(ctx context.Context, r diskRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h, err := openHandle(r.Path, windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err != nil {
		return err
	}
	if err := windows.FlushFileBuffers(h); err != nil {
		flushErr := fmt.Errorf("flush PhysicalDrive before eject: %w", err)
		closeErr := windows.CloseHandle(h)
		if closeErr != nil {
			closeErr = fmt.Errorf("close PhysicalDrive after flush failure: %w", closeErr)
		}
		return errors.Join(flushErr, closeErr)
	}
	prevent := []byte{0}
	var n uint32
	_ = windows.DeviceIoControl(h, ioctlStorageMediaRemoval, &prevent[0], 1, nil, 0, &n, nil)
	if err := windows.CloseHandle(h); err != nil {
		return fmt.Errorf("close PhysicalDrive before eject: %w", err)
	}
	if r.devInst != 0 {
		if err := requestDeviceEject(r.devInst); err == nil {
			return nil
		}
	}
	h, err = openHandle(r.Path, windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err != nil {
		return err
	}
	ejectErr := ioctl(h, ioctlStorageEjectMedia, nil)
	closeErr := windows.CloseHandle(h)
	if closeErr != nil {
		closeErr = fmt.Errorf("close PhysicalDrive after eject fallback: %w", closeErr)
	}
	return errors.Join(ejectErr, closeErr)
}

// Native PnP APIs avoid relying on WMI or an external process.
var (
	setupapi            = windows.NewLazySystemDLL("setupapi.dll")
	setupGetClassDevs   = setupapi.NewProc("SetupDiGetClassDevsW")
	setupEnumInterfaces = setupapi.NewProc("SetupDiEnumDeviceInterfaces")
	setupGetDetail      = setupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	setupDestroy        = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
	cfgmgr              = windows.NewLazySystemDLL("cfgmgr32.dll")
	cmGetParent         = cfgmgr.NewProc("CM_Get_Parent")
	cmGetDeviceID       = cfgmgr.NewProc("CM_Get_Device_IDW")
	cmRequestEject      = cfgmgr.NewProc("CM_Request_Device_EjectW")
	diskInterfaceGUID   = windows.GUID{Data1: 0x53f56307, Data2: 0xb6bf, Data3: 0x11d0, Data4: [8]byte{0x94, 0xf2, 0, 0xa0, 0xc9, 0x1e, 0xfb, 0x8b}}
)

type pnpDisk struct {
	instance string
	devInst  uint32
	usb      bool
}

// enumeratePnPDisks maps every present disk-interface device to its disk number. A
// failure mid-enumeration discards the partial map so callers cannot mistake
// it for the complete topology.
var enumeratePnPDisks = func() (map[uint32]pnpDisk, error) {
	h, _, e := setupGetClassDevs.Call(uintptr(unsafe.Pointer(&diskInterfaceGUID)), 0, 0, uintptr(windows.DIGCF_PRESENT|windows.DIGCF_DEVICEINTERFACE))
	if h == uintptr(windows.InvalidHandle) {
		return nil, fmt.Errorf("SetupDiGetClassDevsW: %w", e)
	}
	defer setupDestroy.Call(h)
	out := map[uint32]pnpDisk{}
	for index := uint32(0); ; index++ {
		n, disk, found, done, err := setupDiskAtIndex(h, index)
		if err != nil {
			return nil, fmt.Errorf("SetupDiEnumDeviceInterfaces index %d: %w", index, err)
		}
		if done {
			break
		}
		if found {
			out[n] = disk
		}
	}
	return out, nil
}

func setupDiskAtIndex(set uintptr, index uint32) (uint32, pnpDisk, bool, bool, error) {
	structureSize := setupStructureSize()
	iface := make([]byte, structureSize)
	binary.LittleEndian.PutUint32(iface, uint32(structureSize))
	r, _, callErr := setupEnumInterfaces.Call(set, 0, uintptr(unsafe.Pointer(&diskInterfaceGUID)), uintptr(index), uintptr(unsafe.Pointer(&iface[0])))
	if r == 0 {
		if callErr == windows.ERROR_NO_MORE_ITEMS {
			return 0, pnpDisk{}, false, true, nil
		}
		return 0, pnpDisk{}, false, false, callErr
	}
	detail, devinfo, ok := setupInterfaceDetail(set, iface)
	if !ok {
		return 0, pnpDisk{}, false, false, nil
	}
	path := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(&detail[4])))
	dh, err := openHandle(path, 0)
	if err != nil {
		return 0, pnpDisk{}, false, false, nil
	}
	defer windows.CloseHandle(dh)
	b, err := query(dh, ioctlStorageGetDeviceNumber, 12)
	if err != nil || len(b) < 8 {
		return 0, pnpDisk{}, false, false, nil
	}
	devInst := binary.LittleEndian.Uint32(devinfo[20:24])
	instance, usb := deviceTreeIdentity(devInst)
	return binary.LittleEndian.Uint32(b[4:8]), pnpDisk{instance, devInst, usb}, true, false, nil
}

func setupInterfaceDetail(set uintptr, iface []byte) ([]byte, []byte, bool) {
	var needed uint32
	setupGetDetail.Call(set, uintptr(unsafe.Pointer(&iface[0])), 0, 0, uintptr(unsafe.Pointer(&needed)), 0)
	if needed < 8 {
		return nil, nil, false
	}
	detail := make([]byte, needed)
	cb := uint32(8)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		cb = 6
	}
	binary.LittleEndian.PutUint32(detail, cb)
	devinfo := make([]byte, setupStructureSize())
	binary.LittleEndian.PutUint32(devinfo, uint32(len(devinfo)))
	r, _, _ := setupGetDetail.Call(set, uintptr(unsafe.Pointer(&iface[0])), uintptr(unsafe.Pointer(&detail[0])), uintptr(len(detail)), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&devinfo[0])))
	return detail, devinfo, r != 0
}

func setupStructureSize() int {
	if unsafe.Sizeof(uintptr(0)) == 4 {
		return 28
	}
	return 32
}

// maxDeviceTreeDepth bounds the walk to the root. Real trees are far
// shallower; hitting the bound leaves usb=false, which fails closed.
const maxDeviceTreeDepth = 32

func deviceTreeIdentity(dev uint32) (string, bool) {
	leaf, usb, cur := "", false, dev
	for depth := 0; depth < maxDeviceTreeDepth && cur != 0; depth++ {
		buf := make([]uint16, 512)
		r, _, _ := cmGetDeviceID.Call(uintptr(cur), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
		if r != 0 {
			break
		}
		id := windows.UTF16ToString(buf)
		if depth == 0 {
			leaf = id
		}
		if strings.HasPrefix(strings.ToUpper(id), `USB\`) {
			usb = true
		}
		var parent uint32
		r, _, _ = cmGetParent.Call(uintptr(unsafe.Pointer(&parent)), uintptr(cur), 0)
		if r != 0 {
			break
		}
		cur = parent
	}
	return leaf, usb
}

func requestDeviceEject(dev uint32) error {
	var veto uint32
	name := make([]uint16, 512)
	r, _, _ := cmRequestEject.Call(uintptr(dev), uintptr(unsafe.Pointer(&veto)), uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)), 0)
	if r != 0 {
		return fmt.Errorf("CM_Request_Device_EjectW CONFIGRET=%d veto=%d %s", r, veto, windows.UTF16ToString(name))
	}
	return nil
}

var (
	kernel              = windows.NewLazySystemDLL("kernel32.dll")
	procFindFirstVolume = kernel.NewProc("FindFirstVolumeW")
	procFindNextVolume  = kernel.NewProc("FindNextVolumeW")
	procFindVolumeClose = kernel.NewProc("FindVolumeClose")
)

var enumerateVolumes = func() ([]string, error) {
	buf := make([]uint16, 1024)
	h, _, e := procFindFirstVolume.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if h == uintptr(windows.InvalidHandle) {
		return nil, e
	}
	defer procFindVolumeClose.Call(h)
	var out []string
	for {
		out = append(out, windows.UTF16ToString(buf))
		r, _, e := procFindNextVolume.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if r == 0 {
			if e == windows.ERROR_NO_MORE_FILES {
				break
			}
			return nil, e
		}
	}
	return out, nil
}

const maxVolumeExtentBuffer = 1 << 20

// ioctlCall fills out and reports the returned byte count.
type ioctlCall func(out []byte) (uint32, error)

var callVolumeExtentIOCTL = func(h windows.Handle, b []byte) (uint32, error) {
	var n uint32
	err := windows.DeviceIoControl(h, ioctlVolumeGetVolumeDiskExtents, nil, 0, &b[0], uint32(len(b)), &n, nil)
	return n, err
}

func queryVolumeExtents(call ioctlCall) ([]byte, error) {
	return queryGrowingBuffer(maxVolumeExtentBuffer, call)
}

func queryGrowingBuffer(limit int, call ioctlCall) ([]byte, error) {
	for size := 256; size <= limit; size *= 2 {
		b := make([]byte, size)
		n, err := call(b)
		if err == nil {
			if n > uint32(len(b)) {
				return nil, fmt.Errorf("invalid IOCTL byte count %d for %d-byte buffer", n, len(b))
			}
			return b[:n], nil
		}
		if !bufferTooSmall(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("IOCTL response exceeds %d-byte limit: %w", limit, windows.ERROR_INSUFFICIENT_BUFFER)
}

func volumeGUID(v string) string {
	trimmed := strings.TrimSuffix(v, `\`)
	const prefix = `\\?\Volume{`
	if !strings.HasPrefix(trimmed, prefix) || !strings.HasSuffix(trimmed, "}") {
		return "<unknown-volume>"
	}
	if !isGUID(trimmed[len(prefix) : len(trimmed)-1]) {
		return "<unknown-volume>"
	}
	return trimmed
}

func isGUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range []byte(s) {
		if !validGUIDByte(i, c) {
			return false
		}
	}
	return true
}

func validGUIDByte(index int, value byte) bool {
	if isGUIDSeparator(index) {
		return value == '-'
	}
	return isHexByte(value)
}

func isGUIDSeparator(index int) bool {
	return index == 8 || index == 13 || index == 18 || index == 23
}

func isHexByte(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func volumeDisks(v string) ([]uint32, error) {
	h, err := openVolumeHandle(strings.TrimSuffix(v, `\`), 0)
	if err != nil {
		return nil, fmt.Errorf("volume %s open: %w", volumeGUID(v), err)
	}
	defer windows.CloseHandle(h)
	return volumeDisksFromHandle(v, h)
}

func volumeDisksFromHandle(v string, h windows.Handle) ([]uint32, error) {
	b, err := queryVolumeExtents(func(b []byte) (uint32, error) {
		return callVolumeExtentIOCTL(h, b)
	})
	if err != nil {
		return nil, fmt.Errorf("volume %s IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS: %w", volumeGUID(v), err)
	}
	disks, err := parseVolumeDiskExtents(b)
	if err != nil {
		return nil, fmt.Errorf("volume %s IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS response: %w", volumeGUID(v), err)
	}
	return disks, nil
}

func parseVolumeDiskExtents(b []byte) ([]uint32, error) {
	const headerSize, extentSize = 8, 24
	if len(b) < headerSize {
		return nil, fmt.Errorf("short VOLUME_DISK_EXTENTS header: got %d bytes", len(b))
	}
	count := binary.LittleEndian.Uint32(b[:4])
	required := uint64(headerSize) + uint64(count)*extentSize
	if required > uint64(len(b)) {
		return nil, fmt.Errorf("truncated VOLUME_DISK_EXTENTS: count %d requires %d bytes, got %d", count, required, len(b))
	}
	if uint64(count) > uint64((maxVolumeExtentBuffer-headerSize)/extentSize) {
		return nil, fmt.Errorf("VOLUME_DISK_EXTENTS count %d exceeds supported limit", count)
	}
	out := make([]uint32, 0, int(count))
	for off := headerSize; uint32(len(out)) < count; off += extentSize {
		out = append(out, binary.LittleEndian.Uint32(b[off:off+4]))
	}
	return out, nil
}

var (
	openVolumeHandle = openHandle
	queryVolumeDisks = volumeDisks
	querySystemDisks = systemDiskNumbers
)

func volumesForDisk(n uint32) ([]string, error) {
	return volumesForDiskUsing(n, queryVolumeDisks)
}

func volumesForDiskUsing(n uint32, query func(string) ([]uint32, error)) ([]string, error) {
	index, err := diskVolumeIndexUsing(query)
	if err != nil {
		return nil, err
	}
	return index[n], nil
}

func diskVolumeIndex() (map[uint32][]string, error) {
	return diskVolumeIndexUsing(queryVolumeDisks)
}

// diskVolumeIndexUsing walks the volume namespace once and maps each disk to
// the volumes with an extent on it, so enumeration does not repeat the walk
// per disk. A volume with several extents on one disk appears once for that
// disk; locking it twice would fail against its own lock.
func diskVolumeIndexUsing(query func(string) ([]uint32, error)) (map[uint32][]string, error) {
	vs, err := enumerateVolumes()
	if err != nil {
		return nil, fmt.Errorf("%w: enumerate Windows volumes: %w", ErrVolumeTopologyUnavailable, err)
	}
	index := make(map[uint32][]string)
	seen := make(map[string]bool, len(vs))
	for _, v := range vs {
		if seen[v] {
			continue
		}
		seen[v] = true
		disks, err := query(v)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrVolumeTopologyUnavailable, err)
		}
		for _, disk := range uniqueDisks(disks) {
			index[disk] = append(index[disk], v)
		}
	}
	return index, nil
}

func uniqueDisks(disks []uint32) []uint32 {
	out := slices.Clone(disks)
	slices.Sort(out)
	return slices.Compact(out)
}

func systemDiskNumbers() (map[uint32]bool, error) {
	root := os.Getenv("SystemRoot")
	if !validSystemRoot(root) {
		return nil, fmt.Errorf("invalid SystemRoot %q", root)
	}
	drive := `\\.\` + root[:2]
	h, err := openHandle(drive, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	b, err := queryVolumeExtents(func(b []byte) (uint32, error) {
		return callVolumeExtentIOCTL(h, b)
	})
	if err != nil {
		return nil, fmt.Errorf("system volume IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS: %w", err)
	}
	disks, err := parseVolumeDiskExtents(b)
	if err != nil {
		return nil, fmt.Errorf("system volume IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS response: %w", err)
	}
	out := make(map[uint32]bool, len(disks))
	for _, disk := range disks {
		out[disk] = true
	}
	return out, nil
}

func validSystemRoot(root string) bool {
	return len(root) >= 3 && isDriveLetter(root[0]) && root[1] == ':' && isPathSeparator(root[2])
}

func isDriveLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isPathSeparator(value byte) bool {
	return value == '\\' || value == '/'
}

func (a *winAPI) formatFAT32(ctx context.Context, r diskRecord, label string, updates chan<- progress.Update) error {
	f, err := a.openDisk(ctx, r, true)
	if err != nil {
		return err
	}
	defer f.Close()
	target, ok := f.(fat32.Device)
	if !ok {
		return errors.New("raw disk does not support random-access formatting")
	}
	// Progress is best effort: a full channel drops the update rather than
	// stalling the format, and cancellation is observed by fat32.Format itself.
	err = fat32.Format(ctx, target, r.Size, label, func(percent uint64) {
		if updates == nil {
			return
		}
		select {
		case updates <- progress.Update{Stage: progress.StageFormatting, BytesProcessed: percent, TotalBytes: 100}:
		default:
		}
	})
	if err != nil {
		return err
	}
	return target.Sync()
}
