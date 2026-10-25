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
	"golang.org/x/sys/windows"
)

const (
	ioctlStorageQueryProperty       = 0x002d1400
	ioctlStorageGetDeviceNumber     = 0x002d1080
	ioctlStorageGetHotplugInfo      = 0x002d0c14
	ioctlDiskGetLengthInfo          = 0x0007405c
	ioctlDiskGetDriveGeometryEx     = 0x000700a0
	ioctlVolumeGetVolumeDiskExtents = 0x00560000
	ioctlStorageMediaRemoval        = 0x002d4804
	ioctlStorageEjectMedia          = 0x002d4808
	fsctlLockVolume                 = 0x00090018
	fsctlDismountVolume             = 0x00090020
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
	storageHotplugInfoSize = 8
	// STORAGE_HOTPLUG_INFO field offsets; b[4] is MediaRemovable, which admission ignores.
	hotplugMediaOffset       = 5
	hotplugDeviceOffset      = 6
	maxStorageDescriptorSize = 1 << 20
	// DISK_GEOMETRY_EX: a 24-byte DISK_GEOMETRY, then the DiskSize
	// LARGE_INTEGER, then variable partition and detection data.
	diskGeometryExSizeOffset = 24
	// DISK_GEOMETRY.BytesPerSector follows Cylinders (LARGE_INTEGER),
	// MediaType, TracksPerCylinder, and SectorsPerTrack.
	diskGeometryBytesPerSectorOffset = 20
	diskGeometryExQuerySize          = 256

	storageIDCodeSetBinary = 1
	storageIDCodeSetASCII  = 2
	storageIDCodeSetUTF8   = 3
	storageIDTypeEUI64     = 2
	storageIDTypeFCPHName  = 3
	storageIDAssocDevice   = 0
)

type winAPI struct{}

// diskNumber is a PhysicalDrive number, as IOCTL_STORAGE_GET_DEVICE_NUMBER and
// VOLUME_DISK_EXTENTS report it.
type diskNumber uint32

func (n diskNumber) path() string {
	return `\\.\PhysicalDrive` + strconv.FormatUint(uint64(n), 10)
}

// devNode is a configuration manager device instance handle (DEVINST).
type devNode uint32

// volumePath is a volume GUID path as FindFirstVolumeW returns it, trailing
// backslash included: \\?\Volume{GUID}\.
type volumePath string

// devicePath drops the trailing backslash, so CreateFile opens the volume
// itself rather than its root directory.
func (v volumePath) devicePath() string { return strings.TrimSuffix(string(v), "\\") }

// volumeDiskQuery reports the disks a volume has extents on.
type volumeDiskQuery func(volumePath) ([]diskNumber, error)

// busType is STORAGE_DEVICE_DESCRIPTOR.BusType.
type busType uint32

const busTypeUSB busType = 7

// storageProperty is a STORAGE_PROPERTY_ID.
type storageProperty uint32

const (
	storageDeviceProperty   storageProperty = 0
	storageDeviceIDProperty storageProperty = 2
)

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

var busNames = map[busType]string{1: "scsi", 3: "ata", 7: "usb", 8: "raid", 11: "sata", 14: "virtual", 15: "filebackedvirtual", 17: "nvme"}

func (b busType) name() string { return busNames[b] }

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

// parseStorageDeviceIDs extracts the WWN from a STORAGE_DEVICE_ID_DESCRIPTOR.
// Conflicting WWNs are rejected because choosing one based on descriptor order
// would make the device identity unstable.
func parseStorageDeviceIDs(b []byte) (string, error) {
	list, err := parseStorageDeviceIDDescriptor(b)
	if err != nil {
		return "", err
	}
	return list.wwn()
}

// storageIdentifierList is a STORAGE_DEVICE_ID_DESCRIPTOR: its bytes and the
// number of STORAGE_IDENTIFIER entries it declares.
type storageIdentifierList struct {
	descriptor []byte
	count      uint32
}

func (l storageIdentifierList) wwn() (string, error) {
	entry := storageIdentifierEntry{descriptor: l.descriptor, offset: storageDeviceIDDescriptorSize}
	var found wwnEvidence
	for ; entry.index < l.count; entry.index++ {
		header, value, err := entry.parse()
		if err != nil {
			return "", err
		}
		found, err = found.merge(value)
		if err != nil {
			return "", err
		}
		if err := l.validateLink(entry, header); err != nil {
			return "", err
		}
		if header.next != 0 {
			entry.offset += header.next
		}
	}
	return string(found), nil
}

// validateLink checks that the entry chain ends exactly at the declared
// count: every entry but the last links onward, and the last does not.
func (l storageIdentifierList) validateLink(entry storageIdentifierEntry, header storageIdentifierHeader) error {
	parsed := entry.index + 1
	if parsed < l.count && header.next == 0 {
		return errors.New("incomplete STORAGE_IDENTIFIER list")
	}
	if parsed == l.count && header.next != 0 {
		return errors.New("STORAGE_IDENTIFIER list exceeds declared count")
	}
	return nil
}

func parseStorageDeviceIDDescriptor(b []byte) (storageIdentifierList, error) {
	if len(b) < storageDeviceIDDescriptorSize {
		return storageIdentifierList{}, fmt.Errorf("short STORAGE_DEVICE_ID_DESCRIPTOR: got %d bytes", len(b))
	}
	size := binary.LittleEndian.Uint32(b[4:8])
	if size < storageDeviceIDDescriptorSize || uint64(size) > uint64(len(b)) {
		return storageIdentifierList{}, fmt.Errorf("invalid STORAGE_DEVICE_ID_DESCRIPTOR size %d for %d bytes", size, len(b))
	}
	return storageIdentifierList{descriptor: b[:size], count: binary.LittleEndian.Uint32(b[8:12])}, nil
}

// wwnEvidence is a WWN read from a STORAGE_IDENTIFIER, or the one agreed so
// far across a descriptor's identifiers.
type wwnEvidence string

// merge rejects conflicting WWNs, because choosing one by descriptor order
// would make the device identity unstable.
func (w wwnEvidence) merge(value wwnEvidence) (wwnEvidence, error) {
	if w.conflicts(value) {
		return "", errors.New("conflicting WWN identifiers")
	}
	if value != "" {
		return value, nil
	}
	return w, nil
}

func (w wwnEvidence) conflicts(value wwnEvidence) bool {
	return w != "" && value != "" && w != value
}

// storageIdentifierEntry locates one STORAGE_IDENTIFIER in its descriptor.
type storageIdentifierEntry struct {
	descriptor []byte
	offset     int
	index      uint32
}

// storageIdentifierHeader is one decoded STORAGE_IDENTIFIER header.
type storageIdentifierHeader struct {
	codeSet, idType, association uint32
	size, next                   int
}

// parse decodes the entry's header and value. Only device-associated NAA and
// EUI identifiers provide stable WWN evidence; other valid identifier types
// are intentionally ignored.
func (e storageIdentifierEntry) parse() (storageIdentifierHeader, wwnEvidence, error) {
	b, off := e.descriptor, e.offset
	if off+storageIdentifierHeaderSize > len(b) {
		return storageIdentifierHeader{}, "", fmt.Errorf("truncated STORAGE_IDENTIFIER header %d", e.index)
	}
	header := storageIdentifierHeader{
		codeSet:     binary.LittleEndian.Uint32(b[off : off+4]),
		idType:      binary.LittleEndian.Uint32(b[off+4 : off+8]),
		size:        int(binary.LittleEndian.Uint16(b[off+8 : off+10])),
		next:        int(binary.LittleEndian.Uint16(b[off+10 : off+12])),
		association: binary.LittleEndian.Uint32(b[off+12 : off+16]),
	}
	if !e.fits(header) {
		return storageIdentifierHeader{}, "", fmt.Errorf("truncated STORAGE_IDENTIFIER entry %d", e.index)
	}
	if !e.validNext(header) {
		return storageIdentifierHeader{}, "", fmt.Errorf("invalid STORAGE_IDENTIFIER next offset %d", header.next)
	}
	if !header.stable() {
		return header, "", nil
	}
	return header, e.value(header), nil
}

func (e storageIdentifierEntry) fits(header storageIdentifierHeader) bool {
	return header.size > 0 && e.offset+storageIdentifierHeaderSize+header.size <= len(e.descriptor)
}

func (e storageIdentifierEntry) validNext(header storageIdentifierHeader) bool {
	return header.next == 0 || (header.next >= storageIdentifierHeaderSize+header.size && e.offset+header.next <= len(e.descriptor))
}

// value renders the identifier bytes in the form its code set calls for.
func (e storageIdentifierEntry) value(header storageIdentifierHeader) wwnEvidence {
	start := e.offset + storageIdentifierHeaderSize
	raw := e.descriptor[start : start+header.size]
	switch header.codeSet {
	case storageIDCodeSetBinary:
		return wwnEvidence(fmt.Sprintf("%X", raw))
	case storageIDCodeSetASCII, storageIDCodeSetUTF8:
		return wwnEvidence(normalizeIdentity(cString(raw)))
	default:
		return ""
	}
}

func (h storageIdentifierHeader) stable() bool {
	return h.association == storageIDAssocDevice && (h.idType == storageIDTypeEUI64 || h.idType == storageIDTypeFCPHName)
}

// storagePropertyIOCTL issues IOCTL_STORAGE_QUERY_PROPERTY with distinct input
// and output buffers so the driver never overwrites the query it is reading.
var storagePropertyIOCTL = func(h windows.Handle, in, out []byte) (uint32, error) {
	var n uint32
	err := windows.DeviceIoControl(h, ioctlStorageQueryProperty, &in[0], uint32(len(in)), &out[0], uint32(len(out)), &n, nil)
	return n, err
}

// request is the STORAGE_PROPERTY_QUERY input buffer for a standard query.
func (p storageProperty) request() []byte {
	in := make([]byte, storagePropertyQuerySize)
	binary.LittleEndian.PutUint32(in[0:4], uint32(p))
	binary.LittleEndian.PutUint32(in[4:8], propertyStandardQuery)
	return in
}

func queryStorageProperty(h windows.Handle, property storageProperty) ([]byte, error) {
	in := property.request()
	return queryStorageDescriptor(func(out []byte) (uint32, error) {
		return storagePropertyIOCTL(h, in, out)
	})
}

// ioctlCall fills out and reports the returned byte count.
type ioctlCall func(out []byte) (uint32, error)

// queryStorageDescriptor performs the documented two-phase query: a
// header-sized buffer yields STORAGE_DESCRIPTOR_HEADER.Size, which then sizes
// the full request. Drivers that report the short buffer as an error instead
// of filling the header fall back to geometric growth.
func queryStorageDescriptor(call ioctlCall) ([]byte, error) {
	size, grow, err := queryStorageDescriptorSize(call)
	if grow {
		return queryGrowingBuffer(maxStorageDescriptorSize, call)
	}
	if err != nil {
		return nil, err
	}
	return querySizedStorageDescriptor(size, call)
}

func queryStorageDescriptorSize(call ioctlCall) (uint32, bool, error) {
	header := make([]byte, storageDescriptorHeaderSize)
	n, err := call(header)
	if bufferTooSmall(err) {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	if n < storageDescriptorHeaderSize {
		return 0, false, fmt.Errorf("short STORAGE_DESCRIPTOR_HEADER: got %d bytes", n)
	}
	size := binary.LittleEndian.Uint32(header[4:8])
	if size < storageDescriptorHeaderSize || size > maxStorageDescriptorSize {
		return 0, false, fmt.Errorf("invalid storage descriptor size %d", size)
	}
	return size, false, nil
}

func querySizedStorageDescriptor(size uint32, call ioctlCall) ([]byte, error) {
	out := make([]byte, size)
	n, err := call(out)
	if err != nil {
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

// storageWWN queries StorageDeviceIdProperty for the disk's WWN.
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
	number     diskNumber
	length     uint64
	descriptor []byte
	wwn        string
	hotplug    hotplugFlags
}

func inspectHandle(h windows.Handle, number diskNumber) (diskRecord, error) {
	if err := verifyDeviceNumber(h, number); err != nil {
		return diskRecord{}, err
	}
	ev, err := gatherDiskEvidence(h, number)
	if err != nil {
		return diskRecord{}, err
	}
	return diskRecordFromEvidence(ev)
}

func gatherDiskEvidence(h windows.Handle, number diskNumber) (diskEvidence, error) {
	length, err := diskLength(h)
	if err != nil {
		return diskEvidence{}, err
	}
	descriptor, err := storageDescriptor(h)
	if err != nil {
		return diskEvidence{}, err
	}
	hotplug, err := hotplugInfo(h)
	if err != nil {
		return diskEvidence{}, err
	}
	wwn, err := storageWWN(h)
	if err != nil {
		return diskEvidence{}, err
	}
	return diskEvidence{number: number, length: length, descriptor: descriptor, hotplug: hotplug, wwn: wwn}, nil
}

func verifyDeviceNumber(h windows.Handle, expected diskNumber) error {
	n, err := query(h, ioctlStorageGetDeviceNumber, 12)
	if err != nil {
		return fmt.Errorf("IOCTL_STORAGE_GET_DEVICE_NUMBER: %w", err)
	}
	if len(n) < 8 {
		return errors.New("short STORAGE_DEVICE_NUMBER")
	}
	if diskNumber(binary.LittleEndian.Uint32(n[4:8])) != expected {
		return ErrDeviceChanged
	}
	return nil
}

// diskLength prefers IOCTL_DISK_GET_LENGTH_INFO, which requires
// FILE_READ_ACCESS. Enumeration opens disks with no access rights, so it falls
// back to IOCTL_DISK_GET_DRIVE_GEOMETRY_EX (FILE_ANY_ACCESS), whose
// DISK_GEOMETRY_EX.DiskSize follows the 24-byte DISK_GEOMETRY.
func diskLength(h windows.Handle) (uint64, error) {
	length, err := query(h, ioctlDiskGetLengthInfo, 8)
	if err == nil && len(length) >= 8 {
		return binary.LittleEndian.Uint64(length), nil
	}
	geometry, geometryErr := query(h, ioctlDiskGetDriveGeometryEx, diskGeometryExQuerySize)
	if geometryErr != nil {
		return 0, fmt.Errorf("IOCTL_DISK_GET_LENGTH_INFO: %v; IOCTL_DISK_GET_DRIVE_GEOMETRY_EX: %w", err, geometryErr)
	}
	return parseDiskGeometryExSize(geometry)
}

func parseDiskGeometryExSize(b []byte) (uint64, error) {
	if len(b) < diskGeometryExSizeOffset+8 {
		return 0, fmt.Errorf("short DISK_GEOMETRY_EX: got %d bytes", len(b))
	}
	return binary.LittleEndian.Uint64(b[diskGeometryExSizeOffset:]), nil
}

// logicalSectorSize reports DISK_GEOMETRY.BytesPerSector, the granularity
// every raw read and write on h must respect.
func logicalSectorSize(h windows.Handle) (uint32, error) {
	geometry, err := query(h, ioctlDiskGetDriveGeometryEx, diskGeometryExQuerySize)
	if err != nil {
		return 0, fmt.Errorf("IOCTL_DISK_GET_DRIVE_GEOMETRY_EX: %w", err)
	}
	return parseDiskGeometryBytesPerSector(geometry)
}

func parseDiskGeometryBytesPerSector(b []byte) (uint32, error) {
	if len(b) < diskGeometryBytesPerSectorOffset+4 {
		return 0, fmt.Errorf("short DISK_GEOMETRY: got %d bytes", len(b))
	}
	return binary.LittleEndian.Uint32(b[diskGeometryBytesPerSectorOffset:]), nil
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

type hotplugFlags struct{ mediaHotplug, deviceHotplug bool }

// hotplugInfo reads STORAGE_HOTPLUG_INFO from a disk handle. Hotplug evidence
// gates admission, so a failed query is an error rather than a silent "not
// removable" verdict.
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
	return hotplugFlags{mediaHotplug: b[hotplugMediaOffset] != 0, deviceHotplug: b[hotplugDeviceOffset] != 0}, nil
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
	bus := busType(binary.LittleEndian.Uint32(q[28:32]))
	// Serial plus the immutable descriptor identity is preferred. Devices that
	// do not expose one fail policy rather than falling back to disk number.
	id := identity.canonicalID()
	path := ev.number.path()
	r := diskRecord{Device: device.Device{ID: id, Path: path, Vendor: vendor, Model: model, Serial: identity.Serial, WWN: identity.WWN, Transport: bus.name(), Major: uint32(ev.number), Size: ev.length}, identity: identity, deviceNumber: ev.number, usbAncestor: bus == busTypeUSB}
	r.mediaHotplug = ev.hotplug.mediaHotplug
	r.deviceHotplug = ev.hotplug.deviceHotplug
	return r, nil
}

// list returns one diskRecord per present PnP disk, plus the number of disks
// that were present but could not be inspected.
// Enumeration fails closed when safety-related topology is unavailable rather
// than returning disks whose eligibility could be misclassified. The PnP tree
// is part of that topology: without it USB attachment would rest on bus type
// alone, and the disk set itself comes from the PnP disk-interface class.
func (a *winAPI) list(ctx context.Context) ([]diskRecord, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	systems, err := querySystemDisks()
	if err != nil {
		return nil, 0, fmt.Errorf("%w: identify Windows system disk: %w", ErrSystemTopologyUnavailable, err)
	}
	pnp, err := enumeratePnPDisks()
	if err != nil {
		return nil, 0, fmt.Errorf("%w: enumerate PnP disks: %w", ErrSystemTopologyUnavailable, err)
	}
	volumes, err := diskVolumeIndex()
	if err != nil {
		return nil, 0, err
	}
	return enumerateDiskRecords(ctx, systems, pnp, volumes)
}

// enumerateDiskRecords reports skipped disks separately from listed ones so a
// disk omitted by inspectDiskNumber stays visible to the user as a count.
func enumerateDiskRecords(ctx context.Context, systems map[diskNumber]bool, pnp map[diskNumber]pnpDisk, volumes map[diskNumber][]volumePath) ([]diskRecord, int, error) {
	var out []diskRecord
	skipped := 0
	for _, n := range slices.Sorted(maps.Keys(pnp)) {
		r, found, err := inspectDiskNumber(ctx, n)
		if err != nil {
			return nil, 0, err
		}
		if !found {
			skipped++
			continue
		}
		out = append(out, r.withTopology(diskTopology{system: systems[n], pnp: pnp[n], mounted: len(volumes[n]) > 0}))
	}
	return out, skipped, nil
}

// diskTopology is what enumeration learns about a disk beyond its own handle.
type diskTopology struct {
	system, mounted bool
	pnp             pnpDisk
}

func (r diskRecord) withTopology(t diskTopology) diskRecord {
	r.IsSystemDisk = t.system
	r.SysfsPath = t.pnp.instance
	r.devInst = t.pnp.devInst
	r.usbAncestor = r.usbAncestor || t.pnp.usb
	r.Mounted = t.mounted
	return r
}

// inspectDiskNumber opens one PhysicalDrive and reports whether it yielded a
// complete record.
// A disk that cannot be inspected is omitted rather than listed with partial
// evidence. Expected causes are media-less readers (ERROR_NOT_READY) and
// drivers that reject identity queries; both fail closed by omission.
func inspectDiskNumber(ctx context.Context, number diskNumber) (diskRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return diskRecord{}, false, err
	}
	h, err := openHandle(number.path(), 0)
	if err != nil {
		return diskRecord{}, false, nil
	}
	defer windows.CloseHandle(h)
	r, err := inspectHandle(h, number)
	return r, err == nil, nil
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

func (a *winAPI) lockVolumes(ctx context.Context, n diskNumber) (volumeLocks, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vols, err := volumesForDisk(n)
	if err != nil {
		return nil, err
	}
	locks := &handleLocks{}
	locked := make(map[volumePath]windows.Handle, len(vols))
	for _, v := range vols {
		h, err := v.lock(ctx)
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

func (v volumePath) lock(ctx context.Context) (windows.Handle, error) {
	if err := ctx.Err(); err != nil {
		return windows.InvalidHandle, err
	}
	h, err := openVolumeHandle(v.devicePath(), windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("%w: volume %s open: %w", ErrVolumeLockDenied, v.guid(), err)
	}
	if err := lockWithRetry(ctx, h); err != nil {
		windows.CloseHandle(h)
		return windows.InvalidHandle, fmt.Errorf("%w: volume %s lock/dismount: %w", ErrVolumeLockDenied, v.guid(), err)
	}
	return h, nil
}

// lockWithRetry locks and dismounts the volume, retrying transient denials.
// FSCTL_LOCK_VOLUME fails with ERROR_ACCESS_DENIED while any other handle to
// the volume is open, which is routine right after insertion (shell
// thumbnails, search indexing, antivirus scans). Those handles close within
// moments, so a short exponential retry turns a spurious failure into success
// without masking a genuine denial.
func lockWithRetry(ctx context.Context, h windows.Handle) error {
	delay := lockRetryDelay
	attempt := 0
	for {
		attempt++
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

func validateLockedVolumes(number diskNumber, expected []volumePath, locked map[volumePath]windows.Handle) error {
	current, err := volumesForDiskUsing(number, func(v volumePath) ([]diskNumber, error) {
		if h, ok := locked[v]; ok {
			return v.disksFromHandle(h)
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

func sameVolumes(a, b []volumePath) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[volumePath]int, len(a))
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
	if err := r.flushForEject(); err != nil {
		return err
	}
	if r.devInst.tryEject() {
		return nil
	}
	return r.ejectMedia()
}

func (r diskRecord) flushForEject() error {
	h, err := openHandle(r.Path, windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err != nil {
		return err
	}
	if err := windows.FlushFileBuffers(h); err != nil {
		return closeAfterFlushFailure(h, err)
	}
	allowMediaRemoval(h)
	if err := windows.CloseHandle(h); err != nil {
		return fmt.Errorf("close PhysicalDrive before eject: %w", err)
	}
	return nil
}

func closeAfterFlushFailure(h windows.Handle, flushErr error) error {
	flushErr = fmt.Errorf("flush PhysicalDrive before eject: %w", flushErr)
	closeErr := windows.CloseHandle(h)
	if closeErr != nil {
		closeErr = fmt.Errorf("close PhysicalDrive after flush failure: %w", closeErr)
	}
	return errors.Join(flushErr, closeErr)
}

func allowMediaRemoval(h windows.Handle) {
	prevent := []byte{0}
	var n uint32
	_ = windows.DeviceIoControl(h, ioctlStorageMediaRemoval, &prevent[0], 1, nil, 0, &n, nil)
}

// tryEject reports whether the PnP manager ejected the device; a zero devNode
// means enumeration found none.
func (d devNode) tryEject() bool {
	if d == 0 {
		return false
	}
	return d.requestEject() == nil
}

func (r diskRecord) ejectMedia() error {
	h, err := openHandle(r.Path, windows.GENERIC_READ|windows.GENERIC_WRITE)
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
	devInst  devNode
	usb      bool
}

// enumeratePnPDisks maps every present disk-interface device to its disk number. A
// failure mid-enumeration discards the partial map so callers cannot mistake
// it for the complete topology.
var enumeratePnPDisks = func() (map[diskNumber]pnpDisk, error) {
	h, _, e := setupGetClassDevs.Call(uintptr(unsafe.Pointer(&diskInterfaceGUID)), 0, 0, uintptr(windows.DIGCF_PRESENT|windows.DIGCF_DEVICEINTERFACE))
	if h == uintptr(windows.InvalidHandle) {
		return nil, fmt.Errorf("SetupDiGetClassDevsW: %w", e)
	}
	defer setupDestroy.Call(h)
	out := map[diskNumber]pnpDisk{}
	var index uint32
	for {
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
		index++
	}
	return out, nil
}

func setupDiskAtIndex(set uintptr, index uint32) (diskNumber, pnpDisk, bool, bool, error) {
	structureSize := setupStructureSize()
	iface := make([]byte, structureSize)
	binary.LittleEndian.PutUint32(iface, uint32(structureSize))
	r, _, callErr := setupEnumInterfaces.Call(set, 0, uintptr(unsafe.Pointer(&diskInterfaceGUID)), uintptr(index), uintptr(unsafe.Pointer(&iface[0])))
	if r == 0 {
		if errors.Is(callErr, windows.ERROR_NO_MORE_ITEMS) {
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
	devInst := devNode(binary.LittleEndian.Uint32(devinfo[20:24]))
	instance, usb := devInst.treeIdentity()
	return diskNumber(binary.LittleEndian.Uint32(b[4:8])), pnpDisk{instance, devInst, usb}, true, false, nil
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

// treeIdentity returns d's device instance ID and whether any ancestor up to
// the root is a USB device.
func (d devNode) treeIdentity() (string, bool) {
	leaf, usb, cur := "", false, d
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
		if strings.HasPrefix(strings.ToUpper(id), "USB\\") {
			usb = true
		}
		var parent devNode
		r, _, _ = cmGetParent.Call(uintptr(unsafe.Pointer(&parent)), uintptr(cur), 0)
		if r != 0 {
			break
		}
		cur = parent
	}
	return leaf, usb
}

func (d devNode) requestEject() error {
	var veto uint32
	name := make([]uint16, 512)
	r, _, _ := cmRequestEject.Call(uintptr(d), uintptr(unsafe.Pointer(&veto)), uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)), 0)
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

var enumerateVolumes = func() ([]volumePath, error) {
	buf := make([]uint16, 1024)
	h, _, e := procFindFirstVolume.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if h == uintptr(windows.InvalidHandle) {
		return nil, fmt.Errorf("FindFirstVolumeW: %w", e)
	}
	defer procFindVolumeClose.Call(h)
	var out []volumePath
	for {
		out = append(out, volumePath(windows.UTF16ToString(buf)))
		r, _, e := procFindNextVolume.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if r == 0 {
			if e == windows.ERROR_NO_MORE_FILES {
				break
			}
			return nil, fmt.Errorf("FindNextVolumeW: %w", e)
		}
	}
	return out, nil
}

const maxVolumeExtentBuffer = 1 << 20

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

// guid returns the volume's \\?\Volume{GUID} name for messages, or a
// placeholder when v is not one, so an unexpected path never reaches a log.
func (v volumePath) guid() string {
	trimmed := v.devicePath()
	const prefix = `\\?\Volume{`
	if !strings.HasPrefix(trimmed, prefix) || !strings.HasSuffix(trimmed, "}") {
		return "<unknown-volume>"
	}
	if !guidText(trimmed[len(prefix) : len(trimmed)-1]).valid() {
		return "<unknown-volume>"
	}
	return trimmed
}

// guidText is a GUID in its 8-4-4-4-12 hex digit form, without braces.
type guidText string

// guidPattern marks where a GUID's hyphens fall; every x is a hex digit.
const guidPattern = "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"

func (g guidText) valid() bool {
	if len(g) != len(guidPattern) {
		return false
	}
	for i := range len(g) {
		if guidPattern[i] == '-' {
			if g[i] != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", rune(g[i])) {
			return false
		}
	}
	return true
}

func (v volumePath) disks() ([]diskNumber, error) {
	h, err := openVolumeHandle(v.devicePath(), 0)
	if err != nil {
		return nil, fmt.Errorf("volume %s open: %w", v.guid(), err)
	}
	defer windows.CloseHandle(h)
	return v.disksFromHandle(h)
}

func (v volumePath) disksFromHandle(h windows.Handle) ([]diskNumber, error) {
	b, err := queryVolumeExtents(func(b []byte) (uint32, error) {
		return callVolumeExtentIOCTL(h, b)
	})
	if err != nil {
		return nil, fmt.Errorf("volume %s IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS: %w", v.guid(), err)
	}
	disks, err := parseVolumeDiskExtents(b)
	if err != nil {
		return nil, fmt.Errorf("volume %s IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS response: %w", v.guid(), err)
	}
	return disks, nil
}

func parseVolumeDiskExtents(b []byte) ([]diskNumber, error) {
	const headerSize, extentSize = 8, 24
	if len(b) < headerSize {
		return nil, fmt.Errorf("short VOLUME_DISK_EXTENTS header: got %d bytes", len(b))
	}
	count := binary.LittleEndian.Uint32(b[:4])
	required := uint64(headerSize) + uint64(count)*extentSize
	if required > uint64(len(b)) {
		return nil, fmt.Errorf("truncated VOLUME_DISK_EXTENTS: count %d requires %d bytes, got %d", count, required, len(b))
	}
	// Defensive duplicate of the length check above: unreachable while the
	// buffer cap bounds len(b), but keeps the limit enforced if headerSize,
	// extentSize, or maxVolumeExtentBuffer are ever retuned.
	if uint64(count) > uint64((maxVolumeExtentBuffer-headerSize)/extentSize) {
		return nil, fmt.Errorf("VOLUME_DISK_EXTENTS count %d exceeds supported limit", count)
	}
	out := make([]diskNumber, 0, int(count))
	for off := headerSize; uint32(len(out)) < count; off += extentSize {
		out = append(out, diskNumber(binary.LittleEndian.Uint32(b[off:off+4])))
	}
	return out, nil
}

var (
	openVolumeHandle                 = openHandle
	queryVolumeDisks volumeDiskQuery = volumePath.disks
	querySystemDisks                 = systemDiskNumbers
)

func volumesForDisk(n diskNumber) ([]volumePath, error) {
	return volumesForDiskUsing(n, queryVolumeDisks)
}

func volumesForDiskUsing(n diskNumber, diskQuery volumeDiskQuery) ([]volumePath, error) {
	index, err := diskVolumeIndexUsing(diskQuery)
	if err != nil {
		return nil, err
	}
	return index[n], nil
}

func diskVolumeIndex() (map[diskNumber][]volumePath, error) {
	return diskVolumeIndexUsing(queryVolumeDisks)
}

// diskVolumeIndexUsing walks the volume namespace once and maps each disk to
// the volumes with an extent on it, so enumeration does not repeat the walk
// per disk. A volume with several extents on one disk appears once for that
// disk; locking it twice would fail against its own lock.
func diskVolumeIndexUsing(diskQuery volumeDiskQuery) (map[diskNumber][]volumePath, error) {
	vs, err := enumerateVolumes()
	if err != nil {
		return nil, fmt.Errorf("%w: enumerate Windows volumes: %w", ErrVolumeTopologyUnavailable, err)
	}
	index := make(map[diskNumber][]volumePath)
	seen := make(map[volumePath]bool, len(vs))
	for _, v := range vs {
		if seen[v] {
			continue
		}
		seen[v] = true
		disks, err := diskQuery(v)
		if volumeNotDiskBacked(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrVolumeTopologyUnavailable, err)
		}
		for _, disk := range uniqueDisks(disks) {
			index[disk] = append(index[disk], v)
		}
	}
	return index, nil
}

// volumeNotDiskBacked reports volume errors that mean the volume has no disk
// extents to lock: optical drives and mounted ISO images reject
// IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS, empty card-reader slots report no
// media, and a volume can vanish between enumeration and open. None of these
// can hold a volume on a disk with readable media, so they must not fail the
// whole enumeration.
func volumeNotDiskBacked(err error) bool {
	for _, benign := range []error{
		windows.ERROR_INVALID_FUNCTION, windows.ERROR_NOT_SUPPORTED,
		windows.ERROR_NOT_READY, windows.ERROR_NO_MEDIA_IN_DRIVE,
		windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND,
		windows.ERROR_DEV_NOT_EXIST, windows.ERROR_UNRECOGNIZED_VOLUME,
	} {
		if errors.Is(err, benign) {
			return true
		}
	}
	return false
}

func uniqueDisks(disks []diskNumber) []diskNumber {
	out := slices.Clone(disks)
	slices.Sort(out)
	return slices.Compact(out)
}

func systemDiskNumbers() (map[diskNumber]bool, error) {
	root := os.Getenv("SystemRoot")
	if !validSystemRoot(root) {
		return nil, fmt.Errorf("invalid SystemRoot %q", root)
	}
	drive := "\\\\.\\" + root[:2]
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
	out := make(map[diskNumber]bool, len(disks))
	for _, disk := range disks {
		out[disk] = true
	}
	return out, nil
}

func validSystemRoot(root string) bool {
	return len(root) >= 3 && isDriveLetter(root[0]) && root[1] == ':' && os.IsPathSeparator(root[2])
}

func isDriveLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}
