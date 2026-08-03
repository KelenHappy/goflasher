//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

type fakeRunner struct {
	properties  map[string]string
	mountInfo   string
	failUnmount bool
}

func (f *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "udevadm" {
		return []byte(f.properties[args[len(args)-1]]), nil
	}
	if name == "udisksctl" && len(args) > 0 && args[0] == "unmount" {
		if f.failUnmount {
			return nil, fmt.Errorf("busy")
		}
		if f.mountInfo != "" {
			_ = os.WriteFile(f.mountInfo, nil, 0600)
		}
		return []byte("unmounted"), nil
	}
	return nil, nil
}

func TestEnumerationFiltersAndMounts(t *testing.T) {
	b, run := fixture(t)
	devices, err := b.ListAllowedDevices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("allowed devices = %d: %+v", len(devices), devices)
	}
	byPath := map[string]bool{}
	for _, d := range devices {
		byPath[filepath.Base(d.Path)] = true
	}
	if !byPath["sdb"] || !byPath["sdc"] {
		t.Fatalf("unexpected allowed devices: %+v", devices)
	}
	flash, _ := b.RefreshDevice(context.Background(), "FLASH123")
	if !flash.Mounted || flash.PartitionCount != 1 || len(flash.MountPoints) != 1 || flash.MountPoints[0] != "/media/My USB" {
		t.Fatalf("flash metadata = %+v", flash)
	}
	card, _ := b.RefreshDevice(context.Background(), "CARD123")
	if !card.IsCardReader {
		t.Fatalf("card not identified: %+v", card)
	}
	all, err := b.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range all {
		if filepath.Base(d.Path) == "nvme0n1" && !d.IsSystemDisk {
			t.Fatal("root disk not marked system")
		}
		if filepath.Base(d.Path) == "sdd" && d.IsAllowed {
			t.Fatal("USB SSD allowed")
		}
		if filepath.Base(d.Path) == "sde" && (!d.IsSystemDisk || d.IsAllowed) {
			t.Fatal("swap disk was not rejected as a system disk")
		}
	}
	_ = run
}

func TestRevalidationDetectsReplacement(t *testing.T) {
	b, run := fixture(t)
	selected, err := b.RefreshDevice(context.Background(), "FLASH123")
	if err != nil {
		t.Fatal(err)
	}
	run.properties[selected.Path] = strings.ReplaceAll(run.properties[selected.Path], "FLASH123", "REPLACED")
	if _, err := b.Revalidate(context.Background(), selected); !errors.Is(err, ErrDeviceChanged) {
		t.Fatalf("error = %v", err)
	}
}

func TestRawOperationsUseIdentityOnlyHelper(t *testing.T) {
	b, _ := fixture(t)
	fake := &fakePrivilegedHelper{readData: "verified"}
	b.helper = fake
	selected, err := b.RefreshDevice(context.Background(), "CARD123")
	if err != nil {
		t.Fatal(err)
	}
	w, err := b.OpenWriter(context.Background(), selected)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "image")
	_ = w.Close()
	r, err := b.OpenReader(context.Background(), selected)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(r)
	_ = r.Close()
	if err := b.Flush(context.Background(), selected); err != nil {
		t.Fatal(err)
	}
	if fake.writes.String() != "image" || string(data) != "verified" || len(fake.requests) != 3 {
		t.Fatalf("helper calls = %+v, write=%q read=%q", fake.requests, fake.writes.String(), data)
	}
	for i, mode := range []operationMode{modeWrite, modeRead, modeFlush} {
		req := fake.requests[i]
		if req.Identity != selected.ID || req.Major != selected.Major || req.Minor != selected.Minor || req.Capacity != selected.Size || req.Mode != mode {
			t.Fatalf("request %d = %+v", i, req)
		}
	}
}

func TestHelperFailureReplacementAndAuthorizationCancellation(t *testing.T) {
	b, run := fixture(t)
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
	run.properties[selected.Path] = strings.ReplaceAll(run.properties[selected.Path], "CARD123", "NEWCARD")
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

func TestUnmountAllMountedPartitions(t *testing.T) {
	b, run := fixture(t)
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
	run.failUnmount = true
	selected, _ = b.RefreshDevice(context.Background(), "FLASH123")
	if err := b.Unmount(context.Background(), selected); !errors.Is(err, ErrUnmountFailed) {
		t.Fatalf("error = %v", err)
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

func TestEnumerationFailsClosedWithoutSwapMetadata(t *testing.T) {
	b, _ := fixture(t)
	b.Swaps = filepath.Join(t.TempDir(), "missing-swaps")
	if _, err := b.ListAllowedDevices(context.Background()); err == nil {
		t.Fatal("enumeration succeeded without swap metadata")
	}
}

func fixture(t *testing.T) (*Backend, *fakeRunner) {
	t.Helper()
	root := t.TempDir()
	class := filepath.Join(root, "sys/class/block")
	devices := filepath.Join(root, "sys/devices")
	dev := filepath.Join(root, "dev")
	os.MkdirAll(class, 0755)
	os.MkdirAll(devices, 0755)
	os.MkdirAll(dev, 0755)
	addDisk := func(name, devno, serial string, usb, removable bool, model string) {
		base := filepath.Join(devices, "pci0000:00")
		if usb {
			base = filepath.Join(base, "usb1", "1-1")
		}
		base = filepath.Join(base, "block", name)
		os.MkdirAll(filepath.Join(base, "device"), 0755)
		write(t, filepath.Join(base, "dev"), devno)
		write(t, filepath.Join(base, "size"), "65536")
		write(t, filepath.Join(base, "removable"), boolDigit(removable))
		write(t, filepath.Join(base, "device/type"), "0")
		write(t, filepath.Join(base, "device/vendor"), "Acme")
		write(t, filepath.Join(base, "device/model"), model)
		write(t, filepath.Join(base, "device/serial"), serial)
		os.Symlink(base, filepath.Join(class, name))
		os.WriteFile(filepath.Join(dev, name), nil, 0600)
	}
	addPart := func(parent, name, devno string) {
		parentReal, _ := filepath.EvalSymlinks(filepath.Join(class, parent))
		base := filepath.Join(parentReal, name)
		os.MkdirAll(base, 0755)
		write(t, filepath.Join(base, "dev"), devno)
		write(t, filepath.Join(base, "partition"), "1")
		os.Symlink(base, filepath.Join(class, name))
		os.WriteFile(filepath.Join(dev, name), nil, 0600)
	}
	addDisk("sdb", "8:16", "FLASH123", true, true, "Thumb Drive")
	addPart("sdb", "sdb1", "8:17")
	addDisk("sdc", "8:32", "CARD123", true, true, "Card Reader")
	addDisk("sdd", "8:48", "SSD123", true, true, "Portable SSD")
	addDisk("sde", "8:64", "SWAP123", true, true, "Thumb Drive")
	addPart("sde", "sde1", "8:65")
	addDisk("nvme0n1", "259:0", "SYS123", false, false, "Internal")
	addPart("nvme0n1", "nvme0n1p1", "259:1")
	mount := filepath.Join(root, "mountinfo")
	writeMountInfo(t, mount)
	swaps := filepath.Join(root, "swaps")
	write(t, swaps, "Filename Type Size Used Priority\n"+filepath.Join(dev, "sde1")+" partition 1024 0 -2\n")
	run := &fakeRunner{mountInfo: mount, properties: map[string]string{filepath.Join(dev, "sdb"): "ID_BUS=usb\nID_SERIAL_SHORT=FLASH123\nID_DRIVE_THUMB=1\n", filepath.Join(dev, "sdc"): "ID_BUS=usb\nID_SERIAL_SHORT=CARD123\nID_DRIVE_FLASH_SD=1\n", filepath.Join(dev, "sdd"): "ID_BUS=usb\nID_SERIAL_SHORT=SSD123\nID_DRIVE_THUMB=1\nID_ATA=1\n", filepath.Join(dev, "sde"): "ID_BUS=usb\nID_SERIAL_SHORT=SWAP123\nID_DRIVE_THUMB=1\n", filepath.Join(dev, "nvme0n1"): "ID_BUS=nvme\nID_SERIAL_SHORT=SYS123\n"}}
	b := &Backend{SysClassBlock: class, MountInfo: mount, Swaps: swaps, DevRoot: dev, runner: run}
	return b, run
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
