//go:build linux

package linux

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/progress"
)

type fakePrivilegedHelper struct {
	requests []privilegedRequest
	err      error
	writes   strings.Builder
	readData string
}

func (f *fakePrivilegedHelper) OpenWriter(_ context.Context, r privilegedRequest) (io.WriteCloser, error) {
	f.requests = append(f.requests, r)
	if f.err != nil {
		return nil, f.err
	}
	return nopWriteCloser{&f.writes}, nil
}
func (f *fakePrivilegedHelper) OpenReader(_ context.Context, r privilegedRequest) (io.ReadCloser, error) {
	f.requests = append(f.requests, r)
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.readData)), nil
}
func (f *fakePrivilegedHelper) Flush(_ context.Context, r privilegedRequest) error {
	f.requests = append(f.requests, r)
	return f.err
}
func (f *fakePrivilegedHelper) FormatFAT32(_ context.Context, r privilegedRequest, _ chan<- progress.Update) error {
	f.requests = append(f.requests, r)
	return f.err
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

type syncBuffer struct {
	strings.Builder
	synced bool
}

func (b *syncBuffer) Sync() error { b.synced = true; return nil }

type fakeUDisks struct {
	mountInfo  string
	unmounted  []string
	poweredOff []string
	err        error
}

func (f *fakeUDisks) Unmount(_ context.Context, device string) error {
	f.unmounted = append(f.unmounted, device)
	if f.err != nil {
		return f.err
	}
	return os.WriteFile(f.mountInfo, nil, 0600)
}

func (f *fakeUDisks) PowerOff(_ context.Context, device string) error {
	f.poweredOff = append(f.poweredOff, device)
	return f.err
}

func TestEnumerationFiltersAndMounts(t *testing.T) {
	b := fixture(t)
	devices, err := b.ListAllowedDevices(context.Background())
	requireNoError(t, err)
	requireDevicePaths(t, devices, "sdb", "sdc")
	flash, _ := b.RefreshDevice(context.Background(), "FLASH123")
	assertMountedFlashMetadata(t, flash)
	card, _ := b.RefreshDevice(context.Background(), "CARD123")
	if !card.IsCardReader {
		t.Fatalf("card not identified: %+v", card)
	}
	all, err := b.list(context.Background())
	requireNoError(t, err)
	devicesByName := indexDevicesByName(all)
	assertSystemDisk(t, requireIndexedDevice(t, devicesByName, "nvme0n1"), "root disk")
	assertRejected(t, requireIndexedDevice(t, devicesByName, "sdd"), "USB SSD")
	assertSystemDisk(t, requireIndexedDevice(t, devicesByName, "sde"), "swap disk")
}

func assertMountedFlashMetadata(t *testing.T, flash device.Device) {
	t.Helper()
	if !flash.Mounted {
		t.Fatalf("flash not mounted: %+v", flash)
	}
	if flash.PartitionCount != 1 {
		t.Fatalf("flash partition count = %d, want 1: %+v", flash.PartitionCount, flash)
	}
	if len(flash.MountPoints) != 1 {
		t.Fatalf("flash mount points = %q, want [/media/My USB]: %+v", flash.MountPoints, flash)
	}
	if flash.MountPoints[0] != "/media/My USB" {
		t.Fatalf("flash mount point = %q, want /media/My USB: %+v", flash.MountPoints[0], flash)
	}
}

func TestSmallGenericUSBStorageFallback(t *testing.T) {
	b := fixture(t)
	setUdevProperties(t, b, "sdb", "ID_BUS=usb\nID_SERIAL_SHORT=FLASH123\nID_USB_DRIVER=usb-storage\n")

	device, err := b.RefreshDevice(context.Background(), "FLASH123")
	requireNoError(t, err)
	if !device.IsAllowed {
		t.Fatalf("small removable usb-storage device rejected: %+v", device)
	}

	real, err := filepath.EvalSymlinks(filepath.Join(b.SysClassBlock, "sdb"))
	requireNoError(t, err)
	write(t, filepath.Join(real, "size"), fmt.Sprint(maxGenericUSBFlashSize/512+1))
	device, err = b.RefreshDevice(context.Background(), "FLASH123")
	requireNoError(t, err)
	if device.IsAllowed {
		t.Fatalf("generic usb-storage device over 128 GB allowed: %+v", device)
	}
}

func requireDevicePaths(t *testing.T, devices []device.Device, paths ...string) {
	t.Helper()
	if len(devices) != len(paths) {
		t.Fatalf("allowed devices = %d: %+v", len(devices), devices)
	}
	found := make(map[string]bool, len(devices))
	for _, d := range devices {
		found[filepath.Base(d.Path)] = true
	}
	for _, path := range paths {
		if !found[path] {
			t.Fatalf("device %q missing from %+v", path, devices)
		}
	}
}

func indexDevicesByName(devices []device.Device) map[string]device.Device {
	indexed := make(map[string]device.Device, len(devices))
	for _, d := range devices {
		indexed[filepath.Base(d.Path)] = d
	}
	return indexed
}

func requireIndexedDevice(t *testing.T, devices map[string]device.Device, name string) device.Device {
	t.Helper()
	d, ok := devices[name]
	if !ok {
		t.Fatalf("device %q was not enumerated", name)
	}
	return d
}

func assertSystemDisk(t *testing.T, d device.Device, description string) {
	t.Helper()
	if !d.IsSystemDisk || d.IsAllowed {
		t.Fatalf("%s was not rejected as a system disk: %+v", description, d)
	}
}

func assertRejected(t *testing.T, d device.Device, description string) {
	t.Helper()
	if d.IsAllowed {
		t.Fatalf("%s was allowed: %+v", description, d)
	}
}

func TestRevalidationDetectsReplacement(t *testing.T) {
	b := fixture(t)
	selected, err := b.RefreshDevice(context.Background(), "FLASH123")
	if err != nil {
		t.Fatal(err)
	}
	setUdevProperties(t, b, "sdb", "ID_BUS=usb\nID_SERIAL_SHORT=REPLACED\nID_DRIVE_THUMB=1\n")
	if _, err := b.Revalidate(context.Background(), selected); !errors.Is(err, ErrDeviceChanged) {
		t.Fatalf("error = %v", err)
	}
}

func TestRawOperationsUseIdentityOnlyHelper(t *testing.T) {
	b := fixture(t)
	fake := &fakePrivilegedHelper{readData: "verified"}
	b.helper = fake
	selected, err := b.RefreshDevice(context.Background(), "CARD123")
	requireNoError(t, err)
	w, err := b.OpenWriter(context.Background(), selected)
	requireNoError(t, err)
	_, _ = io.WriteString(w, "image")
	_ = w.Close()
	r, err := b.OpenReader(context.Background(), selected)
	requireNoError(t, err)
	data, _ := io.ReadAll(r)
	_ = r.Close()
	requireNoError(t, b.Flush(context.Background(), selected))
	assertRawOperationResults(t, fake, data)
	assertHelperRequests(t, fake.requests, selected)
}

func assertRawOperationResults(t *testing.T, fake *fakePrivilegedHelper, data []byte) {
	t.Helper()
	if got := fake.writes.String(); got != "image" {
		t.Fatalf("helper write = %q, want %q", got, "image")
	}
	if got := string(data); got != "verified" {
		t.Fatalf("helper read = %q, want %q", got, "verified")
	}
	if got := len(fake.requests); got != 3 {
		t.Fatalf("helper request count = %d, want 3: %+v", got, fake.requests)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func assertHelperRequests(t *testing.T, requests []privilegedRequest, selected device.Device) {
	t.Helper()
	for i, mode := range []operationMode{modeWrite, modeRead, modeFlush} {
		req := requests[i]
		identityMatches := req.Identity == selected.ID && req.Major == selected.Major && req.Minor == selected.Minor
		operationMatches := req.Capacity == selected.Size && req.Mode == mode
		if !identityMatches || !operationMatches {
			t.Fatalf("request %d = %+v", i, req)
		}
	}
}

func TestHelperFailureReplacementAndAuthorizationCancellation(t *testing.T) {
	b := fixture(t)
	selected, _ := b.RefreshDevice(context.Background(), "CARD123")
	fake := &fakePrivilegedHelper{err: errors.New("helper failed")}
	b.helper = fake
	if _, err := b.OpenWriter(context.Background(), selected); err == nil {
		t.Fatal("helper failure was ignored")
	}
	fake.err = ErrDeviceChanged
	if _, err := b.OpenReader(context.Background(), selected); !errors.Is(err, ErrDeviceChanged) {
		t.Fatalf("replacement error = %v", err)
	}
	fake.err = ErrAuthorizationCanceled
	if err := b.Flush(context.Background(), selected); !errors.Is(err, ErrAuthorizationCanceled) {
		t.Fatalf("authorization error = %v", err)
	}
	// A replacement caught by the unprivileged refresh must never reach the helper.
	before := len(fake.requests)
	setUdevProperties(t, b, "sdc", "ID_BUS=usb\nID_SERIAL_SHORT=NEWCARD\nID_DRIVE_FLASH_SD=1\n")
	if _, err := b.OpenWriter(context.Background(), selected); !errors.Is(err, ErrDeviceChanged) {
		t.Fatalf("pre-helper replacement = %v", err)
	}
	if len(fake.requests) != before {
		t.Fatal("helper called after failed revalidation")
	}
}

func TestPrivilegedProtocolRejectsPathsAndUnknownModes(t *testing.T) {
	env := helperEnvironment{SysDevBlock: t.TempDir(), SysClassBlock: t.TempDir(), MountInfo: filepath.Join(t.TempDir(), "mountinfo"), Swaps: filepath.Join(t.TempDir(), "swaps"), DevRoot: t.TempDir()}
	for _, request := range []string{
		`{"identity":"CARD123","major":8,"minor":32,"capacity":33554432,"mode":"write","path":"/dev/sda"}`,
		`{"identity":"CARD123","major":8,"minor":32,"capacity":33554432,"mode":"erase"}`,
	} {
		if err := runPrivilegedHelper(strings.NewReader(request), io.Discard, io.Discard, env); err == nil {
			t.Fatalf("unsafe request accepted: %s", request)
		}
	}
}

func TestEmbeddedHelperInvocationRequiresExactPrivateArgument(t *testing.T) {
	if !IsEmbeddedHelperInvocation([]string{"usbwriter", embeddedHelperArgument}) {
		t.Fatal("exact embedded helper invocation rejected")
	}
	for _, args := range [][]string{{"usbwriter"}, {"usbwriter", embeddedHelperArgument, "extra"}, {"usbwriter", "--helper"}} {
		if IsEmbeddedHelperInvocation(args) {
			t.Fatalf("unexpected embedded helper invocation accepted: %#v", args)
		}
	}
}

func TestWriteProtocolPreservesBufferedBinaryPayloadAndSyncs(t *testing.T) {
	payload := append([]byte{0x00, 0xff, 0x7f, '\n'}, []byte("binary image payload")...)
	request := privilegedRequest{Identity: "SERIAL", Major: 8, Minor: 32, Capacity: uint64(len(payload)), Mode: modeWrite}
	var wire strings.Builder
	requestData, err := json.Marshal(request)
	requireNoError(t, err)
	_, err = wire.Write(requestData)
	requireNoError(t, err)
	_, err = wire.Write(payload)
	requireNoError(t, err)

	decoder := json.NewDecoder(strings.NewReader(wire.String()))
	var decoded privilegedRequest
	requireNoError(t, decoder.Decode(&decoded))
	target := &syncBuffer{}
	requireNoError(t, writeAndSync(target, decoder.Buffered(), strings.NewReader("")))
	if got := []byte(target.String()); !bytes.Equal(got, payload) {
		t.Fatalf("written payload = %x, want %x", got, payload)
	}
	if !target.synced {
		t.Fatal("write completed without syncing the target descriptor")
	}
}

func TestFlushSyncsBeforeInvalidatingBlockCache(t *testing.T) {
	target := &syncBuffer{}
	invalidated := false
	requireNoError(t, flushAndInvalidate(target, func() error {
		if !target.synced {
			t.Fatal("block cache invalidated before target sync")
		}
		invalidated = true
		return nil
	}))
	if !invalidated {
		t.Fatal("block cache was not invalidated")
	}
}

func TestFlushDoesNotInvalidateAfterSyncFailure(t *testing.T) {
	want := errors.New("sync failed")
	target := syncError{err: want}
	invalidated := false
	err := flushAndInvalidate(target, func() error {
		invalidated = true
		return nil
	})
	if !errors.Is(err, want) || invalidated {
		t.Fatalf("error = %v, invalidated = %v", err, invalidated)
	}
}

type syncError struct{ err error }

func (s syncError) Sync() error { return s.err }

func TestProgressParserHandlesFragmentedLines(t *testing.T) {
	var diagnostics strings.Builder
	updates := make(chan progress.Update, 1)
	parser := &progressParser{builder: &diagnostics, updates: updates}
	for _, chunk := range []string{"PROG", "RESS 25 100\ndiagnostic", " detail\nPROGRESS invalid\n"} {
		written, err := parser.Write([]byte(chunk))
		if err != nil {
			t.Fatalf("Write(%q) error = %v", chunk, err)
		}
		if written != len(chunk) {
			t.Fatalf("Write(%q) = %d bytes, want %d", chunk, written, len(chunk))
		}
	}
	update := <-updates
	assertFormattingProgress(t, update, 25, 100)
	if got, want := diagnostics.String(), "diagnostic detail\nPROGRESS invalid\n"; got != want {
		t.Fatalf("diagnostics = %q, want %q", got, want)
	}
}

func assertFormattingProgress(t *testing.T, update progress.Update, processed, total uint64) {
	t.Helper()
	if update.Stage != progress.StageFormatting {
		t.Fatalf("progress stage = %v, want %v", update.Stage, progress.StageFormatting)
	}
	if update.BytesProcessed != processed {
		t.Fatalf("processed bytes = %d, want %d", update.BytesProcessed, processed)
	}
	if update.TotalBytes != total {
		t.Fatalf("total bytes = %d, want %d", update.TotalBytes, total)
	}
}

func TestBuiltInFAT32FormatterCreatesFilesystemWithoutExternalTools(t *testing.T) {
	const size = uint64(64 << 20)
	file := newDirtyDeviceFile(t, size)
	var formatProgress strings.Builder
	requireNoError(t, makeFAT32(file, size, "GOFLASHER", &formatProgress))
	if got, want := formatProgress.String(), "PROGRESS 10 100\nPROGRESS 15 100\nPROGRESS 25 100\nPROGRESS 80 100\nPROGRESS 90 100\nPROGRESS 100 100\n"; got != want {
		t.Fatalf("format progress = %q, want %q", got, want)
	}

	boot := readAt(t, file, 512, 0)
	assertFAT32BootSector(t, boot)
	fatSectors := binary.LittleEndian.Uint32(boot[36:40])
	rootOffset := int64((32 + 2*uint64(fatSectors)) * 512)
	root := readAt(t, file, 32, rootOffset)
	assertRootVolumeLabel(t, root)
	assertZeroedRegion(t, file, zeroedRegion{size: 4 * 512, offset: 2 * 512, description: "primary"})
	assertZeroedRegion(t, file, zeroedRegion{size: 33 * 512, offset: int64(size - 33*512), description: "backup"})
}

func newDirtyDeviceFile(t *testing.T, size uint64) *os.File {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(t.TempDir(), "device"), os.O_CREATE|os.O_RDWR, 0600)
	requireNoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	requireNoError(t, file.Truncate(int64(size)))
	_, err = file.WriteAt(bytes.Repeat([]byte{0xff}, 33*512), int64(size-33*512))
	requireNoError(t, err)
	_, err = file.WriteAt(bytes.Repeat([]byte{0xff}, 32*512), 0)
	requireNoError(t, err)
	return file
}

func readAt(t *testing.T, file *os.File, size int, offset int64) []byte {
	t.Helper()
	data := make([]byte, size)
	_, err := file.ReadAt(data, offset)
	requireNoError(t, err)
	return data
}

func assertFAT32BootSector(t *testing.T, boot []byte) {
	t.Helper()
	if got := string(boot[82:90]); got != "FAT32   " {
		t.Fatalf("filesystem type = %q, want FAT32", got)
	}
	if got := string(boot[71:82]); got != "GOFLASHER  " {
		t.Fatalf("volume label = %q, want GOFLASHER", got)
	}
	if boot[510] != 0x55 {
		t.Fatalf("first boot signature byte = %x, want 55", boot[510])
	}
	if boot[511] != 0xaa {
		t.Fatalf("boot signature = %x, want 55aa", boot[510:512])
	}
}

func assertRootVolumeLabel(t *testing.T, root []byte) {
	t.Helper()
	if got := string(root[:11]); got != "GOFLASHER  " {
		t.Fatalf("root volume label = %q, want GOFLASHER", got)
	}
	if root[11] != 0x08 {
		t.Fatalf("root volume label attribute = %x, want 08", root[11])
	}
}

type zeroedRegion struct {
	size        int
	offset      int64
	description string
}

func assertZeroedRegion(t *testing.T, file *os.File, region zeroedRegion) {
	t.Helper()
	data := readAt(t, file, region.size, region.offset)
	if !bytes.Equal(data, make([]byte, region.size)) {
		t.Fatalf("stale %s partition metadata was not cleared", region.description)
	}
}

func TestReadUSBAncestorSerial(t *testing.T) {
	root := t.TempDir()
	usb := filepath.Join(root, "usb1", "1-13")
	block := filepath.Join(usb, "1-13:1.0", "host8", "target8:0:0", "8:0:0:0", "block", "sdc")
	requireNoError(t, os.MkdirAll(block, 0755))
	write(t, filepath.Join(usb, "idVendor"), "090c")
	write(t, filepath.Join(usb, "idProduct"), "1000")
	write(t, filepath.Join(usb, "serial"), "AA0OO7RP1MRHMVZW")
	if got := readUSBAncestorAttribute(block, "serial"); got != "AA0OO7RP1MRHMVZW" {
		t.Fatalf("USB ancestor serial = %q", got)
	}
}

func TestMetadataAcceptsMatchingUSBSerialWhenSCSISerialDiffers(t *testing.T) {
	root := t.TempDir()
	usb := filepath.Join(root, "usb1", "1-13")
	real := filepath.Join(usb, "1-13:1.0", "host8", "target8:0:0", "8:0:0:0", "block", "sdc")
	class := filepath.Join(root, "class", "sdc")
	requireNoError(t, os.MkdirAll(filepath.Join(real, "device"), 0755))
	requireNoError(t, os.MkdirAll(class, 0755))
	write(t, filepath.Join(usb, "idVendor"), "090c")
	write(t, filepath.Join(usb, "idProduct"), "1000")
	write(t, filepath.Join(usb, "serial"), "USB-SERIAL")
	write(t, filepath.Join(class, "device", "serial"), "SCSI-SERIAL")
	write(t, filepath.Join(class, "dev"), "8:32")
	write(t, filepath.Join(class, "size"), "65536")
	req := privilegedRequest{Identity: "USB-SERIAL", Serial: "USB-SERIAL", Major: 8, Minor: 32, Capacity: 65536 * 512, Mode: modeWrite}
	if _, _, err := validateDeviceMetadata(req, class, real); err != nil {
		t.Fatalf("matching physical USB serial rejected: %v", err)
	}
	req.Identity, req.Serial = "REPLACED", "REPLACED"
	if _, _, err := validateDeviceMetadata(req, class, real); !errors.Is(err, ErrDeviceChanged) {
		t.Fatalf("replacement error = %v", err)
	}
}

func TestUnmountAllMountedPartitions(t *testing.T) {
	b := fixture(t)
	service := b.udisks.(*fakeUDisks)
	selected, err := b.RefreshDevice(context.Background(), "FLASH123")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Unmount(context.Background(), selected); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(b.MountInfo); len(data) != 0 {
		t.Fatalf("mountinfo remains: %q", data)
	}
	// Restore the mount and verify a backend failure aborts the operation.
	writeMountInfo(t, b.MountInfo)
	service.err = fmt.Errorf("busy")
	selected, _ = b.RefreshDevice(context.Background(), "FLASH123")
	if err := b.Unmount(context.Background(), selected); !errors.Is(err, ErrUnmountFailed) {
		t.Fatalf("error = %v", err)
	}
}

func TestUDisksOperationsUseDirectDBusClient(t *testing.T) {
	b := fixture(t)
	service := b.udisks.(*fakeUDisks)
	fake := &fakePrivilegedHelper{}
	b.helper = fake
	selected, err := b.RefreshDevice(context.Background(), "FLASH123")
	requireNoError(t, err)
	requireNoError(t, b.FormatFAT32(context.Background(), selected, "GOFLASHER", nil))

	assertSinglePath(t, "unmounted", service.unmounted, filepath.Join(b.DevRoot, "sdb1"))
	selected, err = b.RefreshDevice(context.Background(), "CARD123")
	requireNoError(t, err)
	requireNoError(t, b.Eject(context.Background(), selected))
	assertSinglePath(t, "powered-off", service.poweredOff, selected.Path)
	assertFormatRequest(t, fake.requests, "GOFLASHER")
}

func assertSinglePath(t *testing.T, operation string, paths []string, want string) {
	t.Helper()
	if len(paths) != 1 {
		t.Fatalf("%s device count = %d, want 1: %#v", operation, len(paths), paths)
	}
	if paths[0] != want {
		t.Fatalf("%s device = %q, want %q", operation, paths[0], want)
	}
}

func assertFormatRequest(t *testing.T, requests []privilegedRequest, label string) {
	t.Helper()
	if len(requests) != 1 {
		t.Fatalf("format helper request count = %d, want 1: %#v", len(requests), requests)
	}
	if requests[0].Mode != modeFormatFAT32 {
		t.Fatalf("format helper mode = %q, want %q", requests[0].Mode, modeFormatFAT32)
	}
	if requests[0].Label != label {
		t.Fatalf("format helper label = %q, want %q", requests[0].Label, label)
	}
}

func TestParseMountInfo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mountinfo")
	os.WriteFile(p, []byte("42 1 8:17 / /media/My\\040USB rw - vfat /dev/sdb1 rw\n"), 0600)
	m, err := parseMountInfo(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := m[devNumber{8, 17}][0]; got != "/media/My USB" {
		t.Fatalf("mount = %q", got)
	}
}

func TestUdevPropertiesReadRuntimeDatabaseWithoutCLI(t *testing.T) {
	root := t.TempDir()
	b := &Backend{UdevDataRoot: root}
	write(t, filepath.Join(root, "b8:16"), "I:ignored\nE:ID_BUS=usb\nE:ID_SERIAL_SHORT=SERIAL=WITH=EQUALS\nE:MALFORMED\n")

	properties := b.udev(8, 16)
	if properties["ID_BUS"] != "usb" || properties["ID_SERIAL_SHORT"] != "SERIAL=WITH=EQUALS" {
		t.Fatalf("udev properties = %#v", properties)
	}
	if _, ok := properties["I:ignored"]; ok {
		t.Fatal("non-environment udev record was parsed as a property")
	}
	if got := b.udev(8, 99); len(got) != 0 {
		t.Fatalf("missing udev record = %#v, want empty supplementary metadata", got)
	}
}

func TestEnumerationFailsClosedWithoutSwapMetadata(t *testing.T) {
	b := fixture(t)
	b.Swaps = filepath.Join(t.TempDir(), "missing-swaps")
	if _, err := b.ListAllowedDevices(context.Background()); err == nil {
		t.Fatal("enumeration succeeded without swap metadata")
	}
}

func fixture(t *testing.T) *Backend {
	t.Helper()
	root := t.TempDir()
	class := filepath.Join(root, "sys/class/block")
	devices := filepath.Join(root, "sys/devices")
	dev := filepath.Join(root, "dev")
	udevData := filepath.Join(root, "run/udev/data")
	for _, path := range []string{class, devices, dev, udevData} {
		requireNoError(t, os.MkdirAll(path, 0755))
	}
	addFixtureDevices(t, class, devices, dev)
	mount := filepath.Join(root, "mountinfo")
	writeMountInfo(t, mount)
	swaps := filepath.Join(root, "swaps")
	write(t, swaps, "Filename Type Size Used Priority\n"+filepath.Join(dev, "sde1")+" partition 1024 0 -2\n")
	b := &Backend{SysClassBlock: class, MountInfo: mount, Swaps: swaps, DevRoot: dev, UdevDataRoot: udevData, udisks: &fakeUDisks{mountInfo: mount}}
	setUdevProperties(t, b, "sdb", "ID_BUS=usb\nID_SERIAL_SHORT=FLASH123\nID_DRIVE_THUMB=1\n")
	setUdevProperties(t, b, "sdc", "ID_BUS=usb\nID_SERIAL_SHORT=CARD123\nID_DRIVE_FLASH_SD=1\n")
	setUdevProperties(t, b, "sdd", "ID_BUS=usb\nID_SERIAL_SHORT=SSD123\nID_DRIVE_THUMB=1\nID_ATA=1\n")
	setUdevProperties(t, b, "sde", "ID_BUS=usb\nID_SERIAL_SHORT=SWAP123\nID_DRIVE_THUMB=1\n")
	setUdevProperties(t, b, "nvme0n1", "ID_BUS=nvme\nID_SERIAL_SHORT=SYS123\n")
	return b
}

func setUdevProperties(t *testing.T, b *Backend, name, properties string) {
	t.Helper()
	deviceNumber := readTrim(filepath.Join(b.SysClassBlock, name, "dev"))
	lines := strings.Split(strings.TrimSpace(properties), "\n")
	for i := range lines {
		lines[i] = "E:" + lines[i]
	}
	write(t, filepath.Join(b.UdevDataRoot, "b"+deviceNumber), strings.Join(lines, "\n")+"\n")
}

func addFixtureDevices(t *testing.T, class, devices, dev string) {
	t.Helper()
	paths := fixturePaths{class: class, devices: devices, dev: dev}
	addFixtureDisk(t, paths, fixtureDisk{name: "sdb", devno: "8:16", serial: "FLASH123", usb: true, removable: true, model: "Thumb Drive"})
	addFixturePartition(t, paths, fixturePartition{parent: "sdb", name: "sdb1", devno: "8:17"})
	addFixtureDisk(t, paths, fixtureDisk{name: "sdc", devno: "8:32", serial: "CARD123", usb: true, removable: true, model: "Card Reader"})
	addFixtureDisk(t, paths, fixtureDisk{name: "sdd", devno: "8:48", serial: "SSD123", usb: true, removable: true, model: "Portable SSD"})
	addFixtureDisk(t, paths, fixtureDisk{name: "sde", devno: "8:64", serial: "SWAP123", usb: true, removable: true, model: "Thumb Drive"})
	addFixturePartition(t, paths, fixturePartition{parent: "sde", name: "sde1", devno: "8:65"})
	addFixtureDisk(t, paths, fixtureDisk{name: "nvme0n1", devno: "259:0", serial: "SYS123", model: "Internal"})
	addFixturePartition(t, paths, fixturePartition{parent: "nvme0n1", name: "nvme0n1p1", devno: "259:1"})
}

type fixturePaths struct {
	class   string
	devices string
	dev     string
}

type fixtureDisk struct {
	name      string
	devno     string
	serial    string
	model     string
	usb       bool
	removable bool
}

func addFixtureDisk(t *testing.T, paths fixturePaths, disk fixtureDisk) {
	t.Helper()
	base := filepath.Join(paths.devices, "pci0000:00")
	if disk.usb {
		base = filepath.Join(base, "usb1", "1-1")
	}
	base = filepath.Join(base, "block", disk.name)
	requireNoError(t, os.MkdirAll(filepath.Join(base, "device"), 0755))
	for path, value := range map[string]string{"dev": disk.devno, "size": "65536", "removable": boolDigit(disk.removable), "device/type": "0", "device/vendor": "Acme", "device/model": disk.model, "device/serial": disk.serial} {
		write(t, filepath.Join(base, path), value)
	}
	requireNoError(t, os.Symlink(base, filepath.Join(paths.class, disk.name)))
	requireNoError(t, os.WriteFile(filepath.Join(paths.dev, disk.name), nil, 0600))
}

type fixturePartition struct {
	parent string
	name   string
	devno  string
}

func addFixturePartition(t *testing.T, paths fixturePaths, partition fixturePartition) {
	t.Helper()
	parentReal, err := filepath.EvalSymlinks(filepath.Join(paths.class, partition.parent))
	requireNoError(t, err)
	base := filepath.Join(parentReal, partition.name)
	requireNoError(t, os.MkdirAll(base, 0755))
	write(t, filepath.Join(base, "dev"), partition.devno)
	write(t, filepath.Join(base, "partition"), "1")
	requireNoError(t, os.Symlink(base, filepath.Join(paths.class, partition.name)))
	requireNoError(t, os.WriteFile(filepath.Join(paths.dev, partition.name), nil, 0600))
}
func writeMountInfo(t *testing.T, path string) {
	t.Helper()
	write(t, path, "42 1 8:17 / /media/My\\040USB rw - vfat /dev/sdb1 rw\n43 1 259:1 / / rw - ext4 /dev/nvme0n1p1 rw\n")
}
func write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
func boolDigit(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
