//go:build darwin

package macos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/goflasher/goflasher/internal/device"
)

type runnerResult struct {
	output []byte
	err    error
}
type fakeRunner struct {
	json        map[string][]byte
	runs        [][]string
	jsonResults map[string][]runnerResult
	runResults  []runnerResult
	deadlines   []bool
}

func (f *fakeRunner) JSON(ctx context.Context, args ...string) ([]byte, error) {
	_, hasDeadline := ctx.Deadline()
	f.deadlines = append(f.deadlines, hasDeadline)
	key := fmt.Sprint(args)
	if results := f.jsonResults[key]; len(results) > 0 {
		result := results[0]
		f.jsonResults[key] = results[1:]
		return result.output, result.err
	}
	out, ok := f.json[key]
	if !ok {
		return nil, fmt.Errorf("unexpected JSON command: %v", args)
	}
	return out, nil
}
func (f *fakeRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	_, hasDeadline := ctx.Deadline()
	f.deadlines = append(f.deadlines, hasDeadline)
	f.runs = append(f.runs, append([]string(nil), args...))
	if len(f.runResults) > 0 {
		result := f.runResults[0]
		f.runResults = f.runResults[1:]
		return result.output, result.err
	}
	return nil, nil
}

func TestListAllowedDevices(t *testing.T) {
	runner := &fakeRunner{json: map[string][]byte{
		"[list external physical]": []byte(`{"AllDisksAndPartitions":[{"DeviceIdentifier":"disk4","Partitions":[{"MountPoint":"/Volumes/USB"}]}]}`),
		"[info disk4]":             []byte(`{"DeviceIdentifier":"disk4","DeviceNode":"/dev/disk4","DeviceTreePath":"IOService:/USB/disk@1","MediaName":"USB Flash","BusProtocol":"USB","TotalSize":16000000000,"Whole":true,"Internal":false,"RemovableMedia":true,"Ejectable":true}`),
	}}
	backend := &Backend{runner: runner}
	devices, err := backend.ListAllowedDevices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}
	assertAllowedDevice(t, devices[0])
}

func assertAllowedDevice(t *testing.T, got device.Device) {
	t.Helper()
	if got.ID != "IOService:/USB/disk@1" {
		t.Fatalf("device ID = %q", got.ID)
	}
	if got.Path != "/dev/rdisk4" {
		t.Fatalf("device path = %q", got.Path)
	}
	if !got.Mounted {
		t.Fatal("device is not mounted")
	}
	if !got.IsAllowed {
		t.Fatal("device is not allowed")
	}
}

const diskList = `{"AllDisksAndPartitions":[{"DeviceIdentifier":"disk4"}]}`
const allowedInfo = `{"DeviceIdentifier":"disk4","DeviceNode":"/dev/disk4","DeviceTreePath":"IOService:/USB/disk@1","MediaName":"USB Flash","BusProtocol":"USB","TotalSize":16000000000,"Whole":true,"Internal":false,"RemovableMedia":true}`

func sequencedRunner(infos ...string) *fakeRunner {
	results := make([]runnerResult, len(infos))
	for i, info := range infos {
		results[i] = runnerResult{output: []byte(info)}
	}
	listResults := make([]runnerResult, len(infos))
	for i := range listResults {
		listResults[i] = runnerResult{output: []byte(diskList)}
	}
	return &fakeRunner{jsonResults: map[string][]runnerResult{
		"[list external physical]": listResults,
		"[info disk4]":             results,
	}}
}

func TestDestructiveOperationsRejectChangedDevice(t *testing.T) {
	changes := map[string]string{
		"identity":    strings.Replace(allowedInfo, "IOService:/USB/disk@1", "IOService:/USB/disk@2", 1),
		"capacity":    strings.Replace(allowedInfo, "16000000000", "32000000000", 1),
		"model":       strings.Replace(allowedInfo, "USB Flash", "Replacement", 1),
		"disk number": strings.Replace(allowedInfo, `"DeviceIdentifier":"disk4"`, `"DeviceIdentifier":"disk5"`, 1),
		"device path": strings.Replace(allowedInfo, "/dev/disk4", "/dev/disk5", 1),
	}
	operations := map[string]func(*Backend, device.Device) error{
		"Unmount":     func(b *Backend, d device.Device) error { return b.Unmount(context.Background(), d) },
		"OpenWriter":  func(b *Backend, d device.Device) error { _, err := b.OpenWriter(context.Background(), d); return err },
		"FormatFAT32": func(b *Backend, d device.Device) error { return b.FormatFAT32(context.Background(), d, "TEST", nil) },
	}
	for change, info := range changes {
		for operation, run := range operations {
			t.Run(change+"/"+operation, func(t *testing.T) {
				runner := sequencedRunner(allowedInfo, info)
				backend := &Backend{runner: runner}
				selected, err := backend.ListAllowedDevices(context.Background())
				if err != nil || len(selected) != 1 {
					t.Fatalf("select device: %v, %v", selected, err)
				}
				if err := run(backend, selected[0]); !errors.Is(err, ErrDeviceChanged) {
					t.Fatalf("error = %v", err)
				}
				if len(runner.runs) != 0 {
					t.Fatalf("destructive commands = %v", runner.runs)
				}
			})
		}
	}
}

func TestListAllowedDevicesFailsClosed(t *testing.T) {
	unsafe := map[string]string{
		"internal":       strings.Replace(allowedInfo, `"Internal":false`, `"Internal":true`, 1),
		"non-removable":  strings.Replace(allowedInfo, `"RemovableMedia":true`, `"RemovableMedia":false`, 1),
		"non-USB":        strings.Replace(allowedInfo, `"BusProtocol":"USB"`, `"BusProtocol":"SATA"`, 1),
		"empty identity": strings.Replace(allowedInfo, "IOService:/USB/disk@1", "", 1),
	}
	for name, info := range unsafe {
		t.Run(name, func(t *testing.T) {
			devices, err := (&Backend{runner: sequencedRunner(info)}).ListAllowedDevices(context.Background())
			assertNoDevices(t, devices)
			if err != nil {
				t.Fatalf("unexpected enumeration error: %v", err)
			}
		})
	}
	t.Run("malformed list JSON", testMalformedListJSON)
	t.Run("list command failure", testListCommandFailure)
	t.Run("malformed info JSON", testMalformedInfoJSON)
}

func testMalformedListJSON(t *testing.T) {
	runner := &fakeRunner{jsonResults: map[string][]runnerResult{"[list external physical]": {{output: []byte("{bad")}}}}
	devices, err := (&Backend{runner: runner}).ListAllowedDevices(context.Background())
	assertEnumerationFailure(t, devices, err)
}

func testListCommandFailure(t *testing.T) {
	cause := errors.New("diskutil failed")
	runner := &fakeRunner{jsonResults: map[string][]runnerResult{"[list external physical]": {{err: cause}}}}
	devices, err := (&Backend{runner: runner}).ListAllowedDevices(context.Background())
	assertNoDevices(t, devices)
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want cause %v", err, cause)
	}
}

func testMalformedInfoJSON(t *testing.T) {
	devices, err := (&Backend{runner: sequencedRunner("{bad")}).ListAllowedDevices(context.Background())
	assertEnumerationFailure(t, devices, err)
}

func assertEnumerationFailure(t *testing.T, devices []device.Device, err error) {
	t.Helper()
	assertNoDevices(t, devices)
	if err == nil {
		t.Fatal("enumeration unexpectedly succeeded")
	}
}

func assertNoDevices(t *testing.T, devices []device.Device) {
	t.Helper()
	if len(devices) != 0 {
		t.Fatalf("devices = %v, want none", devices)
	}
}

func TestCommandErrorsPreserveCauseAndOutput(t *testing.T) {
	cause := errors.New("command failed")
	tests := []struct {
		name   string
		run    func(*Backend, device.Device) error
		prefix string
	}{
		{name: "unmount", run: func(b *Backend, d device.Device) error { return b.Unmount(context.Background(), d) }, prefix: "unmount failed"},
		{name: "eject", run: func(b *Backend, d device.Device) error { return b.Eject(context.Background(), d) }, prefix: "eject:"},
		{name: "format", run: func(b *Backend, d device.Device) error { return b.FormatFAT32(context.Background(), d, "LABEL", nil) }, prefix: "format FAT32:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := sequencedRunner(allowedInfo, allowedInfo)
			runner.runResults = []runnerResult{{output: []byte("diskutil output"), err: cause}}
			backend := &Backend{runner: runner}
			selected, _ := backend.ListAllowedDevices(context.Background())
			err := tt.run(backend, selected[0])
			assertCommandError(t, err, cause, tt.prefix, "diskutil output")
		})
	}
}

func assertCommandError(t *testing.T, err, cause error, prefix, output string) {
	t.Helper()
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want cause %v", err, cause)
	}
	if !strings.Contains(err.Error(), output) {
		t.Fatalf("error = %v, want output %q", err, output)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("error = %v, want prefix %q", err, prefix)
	}
}

func TestEveryOperationRevalidates(t *testing.T) {
	operations := map[string]func(*Backend, device.Device) error{
		"OpenReader": func(b *Backend, d device.Device) error { _, err := b.OpenReader(context.Background(), d); return err },
		"OpenWriter": func(b *Backend, d device.Device) error { _, err := b.OpenWriter(context.Background(), d); return err },
		"Flush":      func(b *Backend, d device.Device) error { return b.Flush(context.Background(), d) },
		"Eject":      func(b *Backend, d device.Device) error { return b.Eject(context.Background(), d) },
	}
	changed := strings.Replace(allowedInfo, "16000000000", "1", 1)
	for name, run := range operations {
		t.Run(name, func(t *testing.T) {
			runner := sequencedRunner(allowedInfo, changed)
			backend := &Backend{runner: runner}
			selected, _ := backend.ListAllowedDevices(context.Background())
			if err := run(backend, selected[0]); !errors.Is(err, ErrDeviceChanged) {
				t.Fatalf("error = %v", err)
			}
			if len(runner.jsonResults["[list external physical]"]) != 0 {
				t.Fatal("operation did not perform its own enumeration")
			}
		})
	}
}

func TestFormatLabelIsSingleArgument(t *testing.T) {
	runner := sequencedRunner(allowedInfo, allowedInfo)
	backend := &Backend{runner: runner}
	selected, _ := backend.ListAllowedDevices(context.Background())
	label := `name;$(touch /tmp/bad) "quoted" 'single'`
	if err := backend.FormatFAT32(context.Background(), selected[0], label, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"eraseDisk", "FAT32", label, "MBRFormat", "/dev/disk4"}
	if fmt.Sprint(runner.runs) != fmt.Sprint([][]string{want}) {
		t.Fatalf("commands = %v, want %v", runner.runs, want)
	}
}

func TestDevicePathConversions(t *testing.T) {
	if got := wholeDevice("/dev/rdisk12"); got != "/dev/disk12" {
		t.Fatalf("wholeDevice() = %q", got)
	}
}

func TestWholeDiskIdentifierValidation(t *testing.T) {
	valid := map[string]uint32{"disk0": 0, "disk12": 12}
	for identifier, want := range valid {
		if got, ok := wholeDiskNumber(identifier); !ok || got != want {
			t.Errorf("wholeDiskNumber(%q) = %d, %v; want %d, true", identifier, got, ok, want)
		}
	}
	for _, identifier := range []string{"", "disk", "disk4s1", "/dev/disk4", "Disk4", "disk-1", "friendly-name"} {
		if got, ok := wholeDiskNumber(identifier); ok {
			t.Errorf("wholeDiskNumber(%q) = %d, true", identifier, got)
		}
	}
}

func TestDeviceFromInfoRequiresMatchingStructuredIdentifiers(t *testing.T) {
	listed := listedDisk{DeviceIdentifier: "disk4"}
	base := infoJSON{DeviceIdentifier: "disk4", DeviceNode: "/dev/disk4", DeviceTreePath: "IOService:/USB/disk@1", BusProtocol: "USB", TotalSize: 1024, Whole: true, RemovableMedia: true}
	d, ok := deviceFromInfo(listed, base)
	if !ok {
		t.Fatal("matching structured identifiers were rejected")
	}
	if d.Path != "/dev/rdisk4" {
		t.Fatalf("device path = %q, want /dev/rdisk4", d.Path)
	}
	if d.Major != 4 {
		t.Fatalf("device number = %d, want 4", d.Major)
	}
	changedIdentifier := base
	changedIdentifier.DeviceIdentifier = "disk5"
	if _, ok := deviceFromInfo(listed, changedIdentifier); ok {
		t.Fatal("mismatched DeviceIdentifier was accepted")
	}
	changedNode := base
	changedNode.DeviceNode = "/dev/disk5"
	if _, ok := deviceFromInfo(listed, changedNode); ok {
		t.Fatal("mismatched DeviceNode was accepted")
	}
}

func TestCommandErrorIncludesStructuredCommandAndStderr(t *testing.T) {
	cause := errors.New("exit status 1")
	err := commandError("diskutil", cause, []byte(" permission denied\n"))
	assertCommandError(t, err, cause, "diskutil", "permission denied")
}

func TestFormatFAT32UsesWholeDeviceAndMBR(t *testing.T) {
	runner := &fakeRunner{json: map[string][]byte{
		"[list external physical]": []byte(`{"AllDisksAndPartitions":[{"DeviceIdentifier":"disk4"}]}`),
		"[info disk4]":             []byte(`{"DeviceIdentifier":"disk4","DeviceNode":"/dev/disk4","DeviceTreePath":"IOService:/USB/disk@1","MediaName":"USB Flash","BusProtocol":"USB","TotalSize":16000000000,"Whole":true,"Internal":false,"RemovableMedia":true}`),
	}}
	backend := &Backend{runner: runner}
	devices, err := backend.ListAllowedDevices(context.Background())
	if err != nil || len(devices) != 1 {
		t.Fatalf("ListAllowedDevices() = %v, %v", devices, err)
	}
	if err := backend.FormatFAT32(context.Background(), devices[0], "GOFLASHER", nil); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprint([]string{"eraseDisk", "FAT32", "GOFLASHER", "MBRFormat", "/dev/disk4"})
	if got := fmt.Sprint(runner.runs[0]); got != want {
		t.Fatalf("format command = %s, want %s", got, want)
	}
}

func TestBackendCommandsAlwaysHaveDeadlines(t *testing.T) {
	runner := sequencedRunner(allowedInfo, allowedInfo)
	backend := &Backend{runner: runner}
	devices, err := backend.ListAllowedDevices(context.Background())
	if err != nil || len(devices) != 1 {
		t.Fatalf("ListAllowedDevices() = %v, %v", devices, err)
	}
	if err := backend.FormatFAT32(context.Background(), devices[0], "GOFLASHER", nil); err != nil {
		t.Fatal(err)
	}
	for i, hasDeadline := range runner.deadlines {
		if !hasDeadline {
			t.Errorf("command %d had no context deadline", i)
		}
	}
}
