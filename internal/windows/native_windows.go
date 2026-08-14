//go:build windows

package windows

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
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
	if n > uint32(len(b)) {
		return nil, fmt.Errorf("invalid IOCTL byte count %d for %d-byte buffer", n, len(b))
	}
	return b[:n], nil
}

func descriptorString(b []byte, off uint32) string {
	if off == 0 || int(off) >= len(b) {
		return ""
	}
	s := b[off:]
	if i := bytesIndex(s, 0); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(string(s))
}
func bytesIndex(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}
func busName(v byte) string {
	return map[byte]string{1: "scsi", 3: "ata", 7: "usb", 8: "raid", 11: "sata", 17: "nvme", 14: "virtual", 15: "filebackedvirtual"}[v]
}

func normalizeIdentity(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// validIdentityComponent is intentionally small and extensible. Placeholder
// blacklists belong here only after hardware evidence demonstrates them.
func validIdentityComponent(s string) bool {
	if s == "" || len(s) > 512 {
		return false
	}
	for _, c := range s {
		if c < 0x21 || c > 0x7e {
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

func parseStorageDeviceIDs(b []byte) (string, error) {
	if len(b) < 12 {
		return "", fmt.Errorf("short STORAGE_DEVICE_ID_DESCRIPTOR: got %d bytes", len(b))
	}
	size := binary.LittleEndian.Uint32(b[4:8])
	count := binary.LittleEndian.Uint32(b[8:12])
	if size < 12 || uint64(size) > uint64(len(b)) {
		return "", fmt.Errorf("invalid STORAGE_DEVICE_ID_DESCRIPTOR size %d for %d bytes", size, len(b))
	}
	off := 12
	var found string
	for i := uint32(0); i < count; i++ {
		if off+8 > int(size) {
			return "", fmt.Errorf("truncated STORAGE_IDENTIFIER header %d", i)
		}
		codeSet, idType := b[off], b[off+1]
		idSize := int(binary.LittleEndian.Uint16(b[off+2 : off+4]))
		next := int(binary.LittleEndian.Uint16(b[off+4 : off+6]))
		association := b[off+6]
		if idSize < 1 || off+8+idSize > int(size) {
			return "", fmt.Errorf("truncated STORAGE_IDENTIFIER entry %d", i)
		}
		if next != 0 && (next < 8+idSize || off+next > int(size)) {
			return "", fmt.Errorf("invalid STORAGE_IDENTIFIER next offset %d", next)
		}
		if association == 0 && (idType == 2 || idType == 3) {
			raw := b[off+8 : off+8+idSize]
			var value string
			if codeSet == 1 {
				value = fmt.Sprintf("%X", raw)
			} else if codeSet == 2 {
				value = normalizeIdentity(string(raw))
			}
			if value != "" {
				if found != "" && found != value {
					return "", errors.New("conflicting WWN identifiers")
				}
				found = value
			}
		}
		if next == 0 {
			if i+1 != count {
				return "", errors.New("incomplete STORAGE_IDENTIFIER list")
			}
			break
		}
		off += next
	}
	return found, nil
}

func storageWWN(h windows.Handle) (string, error) {
	q, err := queryGrowingBuffer(4096, func(q []byte) (uint32, error) {
		binary.LittleEndian.PutUint32(q[:4], storageDeviceIDProperty)
		var returned uint32
		err := windows.DeviceIoControl(h, ioctlStorageQueryProperty, &q[0], 8, &q[0], uint32(len(q)), &returned, nil)
		return returned, err
	})
	if errors.Is(err, windows.ERROR_NOT_SUPPORTED) || errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("query StorageDeviceIdProperty: %w", err)
	}
	return parseStorageDeviceIDs(q)
}

func inspectHandle(h windows.Handle, number uint32) (diskRecord, error) {
	n, err := query(h, ioctlStorageGetDeviceNumber, 12)
	if err != nil {
		return diskRecord{}, fmt.Errorf("IOCTL_STORAGE_GET_DEVICE_NUMBER: %w", err)
	}
	if len(n) < 8 {
		return diskRecord{}, errors.New("short STORAGE_DEVICE_NUMBER")
	}
	got := binary.LittleEndian.Uint32(n[4:8])
	if got != number {
		return diskRecord{}, ErrDeviceChanged
	}
	length, err := query(h, ioctlDiskGetLengthInfo, 8)
	if err != nil {
		return diskRecord{}, fmt.Errorf("IOCTL_DISK_GET_LENGTH_INFO: %w", err)
	}
	if len(length) < 8 {
		return diskRecord{}, errors.New("short GET_LENGTH_INFORMATION")
	}
	q := make([]byte, 1024)
	binary.LittleEndian.PutUint32(q[0:4], storageDeviceProperty)
	var returned uint32
	if err = windows.DeviceIoControl(h, ioctlStorageQueryProperty, &q[0], 8, &q[0], uint32(len(q)), &returned, nil); err != nil {
		return diskRecord{}, fmt.Errorf("IOCTL_STORAGE_QUERY_PROPERTY: %w", err)
	}
	if returned < 36 {
		return diskRecord{}, errors.New("short STORAGE_DEVICE_DESCRIPTOR")
	}
	// STORAGE_HOTPLUG_INFO starts with Size (one DWORD), followed by the
	// MediaRemovable, MediaHotplug, DeviceHotplug, and WriteCacheEnableOverride
	// BOOLEAN fields.
	hot := make([]byte, 12)
	_ = ioctl(h, ioctlStorageGetHotplugInfo, hot)
	serial := descriptorString(q, binary.LittleEndian.Uint32(q[24:28]))
	wwn, err := storageWWN(h)
	if err != nil {
		return diskRecord{}, err
	}
	identity, err := newWindowsIdentity(serial, wwn)
	if err != nil {
		return diskRecord{}, err
	}
	vendor := descriptorString(q, binary.LittleEndian.Uint32(q[12:16]))
	model := descriptorString(q, binary.LittleEndian.Uint32(q[16:20]))
	bus := q[28]
	// Serial plus the immutable descriptor identity is preferred. Devices that
	// do not expose one fail policy rather than falling back to disk number.
	id := identity.canonicalID()
	path := `\\.\PhysicalDrive` + strconv.FormatUint(uint64(number), 10)
	r := diskRecord{Device: device.Device{ID: id, Path: path, Vendor: vendor, Model: model, Serial: identity.Serial, WWN: identity.WWN, Transport: busName(bus), Major: number, Size: binary.LittleEndian.Uint64(length)}, identity: identity, deviceNumber: number, usbAncestor: bus == busTypeUSB}
	if len(hot) >= 7 {
		r.mediaHotplug = hot[5] != 0
		r.deviceHotplug = hot[6] != 0
	}
	return r, nil
}

func (a *winAPI) list(ctx context.Context) ([]diskRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	systems, err := querySystemDisks()
	if err != nil {
		return nil, fmt.Errorf("%w: identify Windows system disk: %w", ErrSystemTopologyUnavailable, err)
	}
	pnp, _ := setupDisks()
	var out []diskRecord
	for i := uint32(0); i < 256; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := `\\.\PhysicalDrive` + strconv.FormatUint(uint64(i), 10)
		h, err := openHandle(path, 0)
		if err != nil {
			if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) || errors.Is(err, windows.ERROR_INVALID_NAME) {
				continue
			}
			continue
		}
		r, e := inspectHandle(h, i)
		windows.CloseHandle(h)
		if e != nil {
			continue
		}
		r.IsSystemDisk = systems[i]
		if p, ok := pnp[i]; ok {
			r.SysfsPath, r.devInst = p.instance, p.devInst
			r.usbAncestor = r.usbAncestor || p.usb
		}
		r.Mounted, e = diskHasVolume(i)
		if e != nil {
			return nil, fmt.Errorf("determine mounted state for PhysicalDrive%d: %w", i, e)
		}
		out = append(out, r)
	}
	return out, nil
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
func (f *winFile) Flush() error { return f.f.Sync() }
func (f *winFile) Sync() error  { return windows.FlushFileBuffers(windows.Handle(f.f.Fd())) }
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
	if err != nil || !sameWindowsIdentity(fresh.identity, r.identity) || fresh.ID != r.ID || fresh.Size != r.Size {
		windows.CloseHandle(h)
		if err != nil {
			return nil, err
		}
		return nil, ErrDeviceChanged
	}
	return &winFile{f: os.NewFile(uintptr(h), r.Path), ctx: ctx}, nil
}

type handleLocks []windows.Handle

func (l handleLocks) Close() error {
	var first error
	for _, h := range l {
		if err := windows.CloseHandle(h); err != nil && first == nil {
			first = err
		}
	}
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
	locks := handleLocks{}
	locked := make(map[string]windows.Handle, len(vols))
	for _, v := range vols {
		if err := ctx.Err(); err != nil {
			locks.Close()
			return nil, err
		}
		h, e := openHandle(strings.TrimSuffix(v, `\`), windows.GENERIC_READ|windows.GENERIC_WRITE)
		if e != nil {
			locks.Close()
			return nil, fmt.Errorf("%w: volume %s open: %w", ErrVolumeLockDenied, volumeGUID(v), e)
		}
		if e = ioctl(h, fsctlLockVolume, nil); e == nil {
			e = ioctl(h, fsctlDismountVolume, nil)
		}
		if e != nil {
			windows.CloseHandle(h)
			locks.Close()
			return nil, fmt.Errorf("%w: volume %s lock/dismount: %w", ErrVolumeLockDenied, volumeGUID(v), e)
		}
		locks = append(locks, h)
		locked[v] = h
	}
	current, err := volumesForDiskUsing(n, func(v string) ([]uint32, error) {
		if h, ok := locked[v]; ok {
			return volumeDisksFromHandle(v, h)
		}
		return queryVolumeDisks(v)
	})
	if err != nil {
		locks.Close()
		return nil, err
	}
	if !sameVolumes(vols, current) {
		locks.Close()
		return nil, fmt.Errorf("%w: volume topology changed while locking", ErrDeviceChanged)
	}
	return locks, nil
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
	defer windows.CloseHandle(h)
	_ = windows.FlushFileBuffers(h)
	prevent := []byte{0}
	var n uint32
	_ = windows.DeviceIoControl(h, ioctlStorageMediaRemoval, &prevent[0], 1, nil, 0, &n, nil)
	if r.devInst != 0 {
		if err := requestDeviceEject(r.devInst); err == nil {
			return nil
		}
	}
	return ioctl(h, ioctlStorageEjectMedia, nil)
}

// SetupAPI supplies PnP identity and topology; cfgmgr32 walks parents and
// requests safe removal without WMI or an external process.
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

func setupDisks() (map[uint32]pnpDisk, error) {
	h, _, e := setupGetClassDevs.Call(uintptr(unsafe.Pointer(&diskInterfaceGUID)), 0, 0, uintptr(windows.DIGCF_PRESENT|windows.DIGCF_DEVICEINTERFACE))
	if h == uintptr(windows.InvalidHandle) {
		return nil, e
	}
	defer setupDestroy.Call(h)
	out := map[uint32]pnpDisk{}
	for index := uint32(0); ; index++ {
		isz := 32
		if unsafe.Sizeof(uintptr(0)) == 4 {
			isz = 28
		}
		iface := make([]byte, isz)
		binary.LittleEndian.PutUint32(iface, uint32(isz))
		r, _, er := setupEnumInterfaces.Call(h, 0, uintptr(unsafe.Pointer(&diskInterfaceGUID)), uintptr(index), uintptr(unsafe.Pointer(&iface[0])))
		if r == 0 {
			if er == windows.ERROR_NO_MORE_ITEMS {
				break
			}
			return out, er
		}
		var needed uint32
		setupGetDetail.Call(h, uintptr(unsafe.Pointer(&iface[0])), 0, 0, uintptr(unsafe.Pointer(&needed)), 0)
		if needed < 8 {
			continue
		}
		detail := make([]byte, needed)
		cb := uint32(8)
		if unsafe.Sizeof(uintptr(0)) == 4 {
			cb = 6
		}
		binary.LittleEndian.PutUint32(detail, cb)
		dsz := 32
		if unsafe.Sizeof(uintptr(0)) == 4 {
			dsz = 28
		}
		devinfo := make([]byte, dsz)
		binary.LittleEndian.PutUint32(devinfo, uint32(dsz))
		r, _, _ = setupGetDetail.Call(h, uintptr(unsafe.Pointer(&iface[0])), uintptr(unsafe.Pointer(&detail[0])), uintptr(len(detail)), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&devinfo[0])))
		if r == 0 {
			continue
		}
		path := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(&detail[4])))
		dh, err := openHandle(path, 0)
		if err != nil {
			continue
		}
		b, err := query(dh, ioctlStorageGetDeviceNumber, 12)
		windows.CloseHandle(dh)
		if err != nil {
			continue
		}
		n := binary.LittleEndian.Uint32(b[4:8])
		di := binary.LittleEndian.Uint32(devinfo[20:24])
		instance, usb := deviceTreeIdentity(di)
		out[n] = pnpDisk{instance, di, usb}
	}
	return out, nil
}
func deviceTreeIdentity(dev uint32) (string, bool) {
	leaf, usb, cur := "", false, dev
	for depth := 0; depth < 16 && cur != 0; depth++ {
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

func allVolumes() ([]string, error) {
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

type extentIOCTL func([]byte) (uint32, error)

var callVolumeExtentIOCTL = func(h windows.Handle, b []byte) (uint32, error) {
	var n uint32
	err := windows.DeviceIoControl(h, ioctlVolumeGetVolumeDiskExtents, nil, 0, &b[0], uint32(len(b)), &n, nil)
	return n, err
}

func queryVolumeExtents(call extentIOCTL) ([]byte, error) {
	return queryGrowingBuffer(maxVolumeExtentBuffer, call)
}

func queryGrowingBuffer(limit int, call extentIOCTL) ([]byte, error) {
	for size := 256; size <= limit; size *= 2 {
		b := make([]byte, size)
		n, err := call(b)
		if err == nil {
			if n > uint32(len(b)) {
				return nil, fmt.Errorf("invalid IOCTL byte count %d for %d-byte buffer", n, len(b))
			}
			return b[:n], nil
		}
		if !errors.Is(err, windows.ERROR_MORE_DATA) && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("IOCTL response exceeds %d-byte limit: %w", limit, windows.ERROR_INSUFFICIENT_BUFFER)
}

func volumeGUID(v string) string {
	trimmed := strings.TrimSuffix(v, `\`)
	const prefix = `\\?\Volume{`
	if strings.HasPrefix(trimmed, prefix) && strings.HasSuffix(trimmed, "}") && isGUID(trimmed[len(prefix):len(trimmed)-1]) {
		return trimmed
	}
	return "<unknown-volume>"
}

func isGUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range []byte(s) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
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
	enumerateVolumes = allVolumes
	queryVolumeDisks = volumeDisks
	querySystemDisks = systemDiskNumbers
)

func volumesForDisk(n uint32) ([]string, error) {
	return volumesForDiskUsing(n, queryVolumeDisks)
}

func volumesForDiskUsing(n uint32, query func(string) ([]uint32, error)) ([]string, error) {
	vs, err := enumerateVolumes()
	if err != nil {
		return nil, fmt.Errorf("%w: enumerate Windows volumes: %w", ErrVolumeTopologyUnavailable, err)
	}
	var out []string
	seen := make(map[string]bool)
	for _, v := range vs {
		ds, e := query(v)
		if e != nil {
			return nil, fmt.Errorf("%w: %w", ErrVolumeTopologyUnavailable, e)
		}
		for _, d := range ds {
			if d == n && !seen[v] {
				out = append(out, v)
				seen[v] = true
				break
			}
		}
	}
	return out, nil
}
func diskHasVolume(n uint32) (bool, error) {
	v, err := volumesForDisk(n)
	return len(v) > 0, err
}
func systemDiskNumbers() (map[uint32]bool, error) {
	root := os.Getenv("SystemRoot")
	if len(root) < 3 || root[1] != ':' || (root[2] != '\\' && root[2] != '/') ||
		(root[0] < 'A' || root[0] > 'Z') && (root[0] < 'a' || root[0] > 'z') {
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
	err = fat32.Format(ctx, target, r.Size, label, func(percent uint64) {
		if updates == nil {
			return
		}
		select {
		case updates <- progress.Update{Stage: progress.StageFormatting, BytesProcessed: percent, TotalBytes: 100}:
		case <-ctx.Done():
		default:
		}
	})
	if err != nil {
		return err
	}
	return target.Sync()
}
