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
	if !flash.Mounted || flash.PartitionCount != 1 || len(flash.MountPoints) != 1 || flash.MountPoints[0] != "/media/My USB" {
		t.Fatalf("flash metadata = %+v", flash)
	}
	card, _ := b.RefreshDevice(context.Background(), "CARD123")
	if !card.IsCardReader {
		t.Fatalf("card not identified: %+v", card)
	}
	all, err := b.list(context.Background())
	requireNoError(t, err)
	for _, d := range all {
		assertEnumerationSafety(t, d)
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

func assertEnumerationSafety(t *testing.T, d device.Device) {
	t.Helper()
	switch filepath.Base(d.Path) {
	case "nvme0n1":
		if !d.IsSystemDisk {
			t.Fatal("root disk not marked system")
		}
	case "sdd":
		if d.IsAllowed {
			t.Fatal("USB SSD allowed")
		}
	case "sde":
		if !d.IsSystemDisk || d.IsAllowed {
			t.Fatal("swap disk was not rejected as a system disk")
		}
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
	if fake.writes.String() != "image" || string(data) != "verified" || len(fake.requests) != 3 {
		t.Fatalf("helper calls = %+v, write=%q read=%q", fake.requests, fake.writes.String(), data)
	}
	assertHelperRequests(t, fake.requests, selected)
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
		if written, err := parser.Write([]byte(chunk)); err != nil || written != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, written, err)
		}
	}
	update := <-updates
	if update.Stage != progress.StageFormatting || update.BytesProcessed != 25 || update.TotalBytes != 100 {
		t.Fatalf("progress update = %+v", update)
	}
	if got, want := diagnostics.String(), "diagnostic detail\nPROGRESS invalid\n"; got != want {
		t.Fatalf("diagnostics = %q, want %q", got, want)
	}
}

func TestBuiltInFAT32FormatterCreatesFilesystemWithoutExternalTools(t *testing.T) {
	const size = uint64(64 << 20)
	path := filepath.Join(t.TempDir(), "device")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	requireNoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	requireNoError(t, file.Truncate(int64(size)))
	_, err = file.WriteAt(bytes.Repeat([]byte{0xff}, 33*512), int64(size-33*512))
	requireNoError(t, err)
	_, err = file.WriteAt(bytes.Repeat([]byte{0xff}, 32*512), 0)
	requireNoError(t, err)
	var formatProgress strings.Builder
	requireNoError(t, formatFAT32(file, size, "GOFLASHER", &formatProgress))
	if got, want := formatProgress.String(), "PROGRESS 10 100\nPROGRESS 15 100\nPROGRESS 25 100\nPROGRESS 80 100\nPROGRESS 90 100\nPROGRESS 100 100\n"; got != want {
		t.Fatalf("format progress = %q, want %q", got, want)
	}

	boot := make([]byte, 512)
	_, err = file.ReadAt(boot, 0)
	requireNoError(t, err)
	if string(boot[82:90]) != "FAT32   " || string(boot[71:82]) != "GOFLASHER  " || boot[510] != 0x55 || boot[511] != 0xaa {
		t.Fatalf("invalid FAT32 boot sector: type=%q label=%q signature=%x", boot[82:90], boot[71:82], boot[510:512])
	}
	fatSectors := binary.LittleEndian.Uint32(boot[36:40])
	rootOffset := int64((32 + 2*uint64(fatSectors)) * 512)
	root := make([]byte, 32)
	_, err = file.ReadAt(root, rootOffset)
	requireNoError(t, err)
	if string(root[:11]) != "GOFLASHER  " || root[11] != 0x08 {
		t.Fatalf("invalid root volume label: %q attribute=%x", root[:11], root[11])
	}
	reservedGap := make([]byte, 4*512)
	_, err = file.ReadAt(reservedGap, 2*512)
	requireNoError(t, err)
	if !bytes.Equal(reservedGap, make([]byte, len(reservedGap))) {
		t.Fatal("stale primary partition metadata was not cleared")
	}
	tail := make([]byte, 33*512)
	_, err = file.ReadAt(tail, int64(size)-int64(len(tail)))
	requireNoError(t, err)
	if !bytes.Equal(tail, make([]byte, len(tail))) {
		t.Fatal("stale backup partition metadata was not cleared")
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

	if len(service.unmounted) != 1 || service.unmounted[0] != filepath.Join(b.DevRoot, "sdb1") {
		t.Fatalf("unmounted devices = %#v", service.unmounted)
	}
	selected, err = b.RefreshDevice(context.Background(), "CARD123")
	requireNoError(t, err)
	requireNoError(t, b.Eject(context.Background(), selected))
	if len(service.poweredOff) != 1 || service.poweredOff[0] != selected.Path {
		t.Fatalf("powered-off devices = %#v", service.poweredOff)
	}
	if len(fake.requests) != 1 || fake.requests[0].Mode != modeFormatFAT32 || fake.requests[0].Label != "GOFLASHER" {
		t.Fatalf("format helper request = %#v", fake.requests)
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
	addFixtureDisk(t, class, devices, dev, "sdb", "8:16", "FLASH123", true, true, "Thumb Drive")
	addFixturePartition(t, class, dev, "sdb", "sdb1", "8:17")
	addFixtureDisk(t, class, devices, dev, "sdc", "8:32", "CARD123", true, true, "Card Reader")
	addFixtureDisk(t, class, devices, dev, "sdd", "8:48", "SSD123", true, true, "Portable SSD")
	addFixtureDisk(t, class, devices, dev, "sde", "8:64", "SWAP123", true, true, "Thumb Drive")
	addFixturePartition(t, class, dev, "sde", "sde1", "8:65")
	addFixtureDisk(t, class, devices, dev, "nvme0n1", "259:0", "SYS123", false, false, "Internal")
	addFixturePartition(t, class, dev, "nvme0n1", "nvme0n1p1", "259:1")
}

func addFixtureDisk(t *testing.T, class, devices, dev, name, devno, serial string, usb, removable bool, model string) {
	t.Helper()
	base := filepath.Join(devices, "pci0000:00")
	if usb {
		base = filepath.Join(base, "usb1", "1-1")
	}
	base = filepath.Join(base, "block", name)
	requireNoError(t, os.MkdirAll(filepath.Join(base, "device"), 0755))
	for path, value := range map[string]string{"dev": devno, "size": "65536", "removable": boolDigit(removable), "device/type": "0", "device/vendor": "Acme", "device/model": model, "device/serial": serial} {
		write(t, filepath.Join(base, path), value)
	}
	requireNoError(t, os.Symlink(base, filepath.Join(class, name)))
	requireNoError(t, os.WriteFile(filepath.Join(dev, name), nil, 0600))
}

func addFixturePartition(t *testing.T, class, dev, parent, name, devno string) {
	t.Helper()
	parentReal, err := filepath.EvalSymlinks(filepath.Join(class, parent))
	requireNoError(t, err)
	base := filepath.Join(parentReal, name)
	requireNoError(t, os.MkdirAll(base, 0755))
	write(t, filepath.Join(base, "dev"), devno)
	write(t, filepath.Join(base, "partition"), "1")
	requireNoError(t, os.Symlink(base, filepath.Join(class, name)))
	requireNoError(t, os.WriteFile(filepath.Join(dev, name), nil, 0600))
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
