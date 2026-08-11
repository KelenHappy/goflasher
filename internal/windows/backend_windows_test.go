//go:build windows

package windows

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/goflasher/goflasher/internal/device"
)

type runnerResult struct {
	output []byte
	err    error
}
type fakeRunner struct {
	output    []byte
	err       error
	script    string
	results   []runnerResult
	scripts   []string
	deadlines []bool
}

func (f *fakeRunner) Output(ctx context.Context, script string) ([]byte, error) {
	_, hasDeadline := ctx.Deadline()
	f.deadlines = append(f.deadlines, hasDeadline)
	f.script = script
	f.scripts = append(f.scripts, script)
	if len(f.results) > 0 {
		result := f.results[0]
		f.results = f.results[1:]
		return result.output, result.err
	}
	return f.output, f.err
}

func TestListAllowedDevices(t *testing.T) {
	runner := &fakeRunner{output: []byte(`[
{"Number":4,"FriendlyName":"USB Flash","SerialNumber":"SERIAL","UniqueId":"USB-ID","Size":16000000000,"BusType":"USB","IsRemovable":true,"PartitionCount":1,"MountPoints":["E:\\"]},
{"Number":0,"FriendlyName":"System SSD","UniqueId":"SYSTEM","Size":1000000000000,"BusType":"NVMe","IsBoot":true,"IsSystem":true}
]`)}
	backend := &Backend{runner: runner}
	devices, err := backend.ListAllowedDevices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("got %d allowed devices, want 1", len(devices))
	}
	assertAllowedDevice(t, devices[0])
}

func assertAllowedDevice(t *testing.T, got device.Device) {
	t.Helper()
	if got.ID != "USB-ID" {
		t.Fatalf("device ID = %q, want USB-ID", got.ID)
	}
	if got.Path != `\\.\PhysicalDrive4` {
		t.Fatalf("device path = %q", got.Path)
	}
	if !got.Mounted {
		t.Fatal("device is not mounted")
	}
	if !got.IsAllowed {
		t.Fatal("device is not allowed")
	}
	if len(got.MountPoints) != 1 {
		t.Fatalf("mount points = %q, want one", got.MountPoints)
	}
	if got.MountPoints[0] != `E:\` {
		t.Fatalf("mount point = %q, want E:\\", got.MountPoints[0])
	}
}

const allowedDisk = `[{"Number":4,"FriendlyName":"USB Flash","SerialNumber":"SERIAL","UniqueId":"USB-ID","Size":16000000000,"BusType":"USB","IsRemovable":true}]`

func TestDestructiveOperationsRejectChangedDevice(t *testing.T) {
	changes := map[string]string{
		"identity":    strings.Replace(allowedDisk, "USB-ID", "OTHER-ID", 1),
		"serial":      strings.Replace(allowedDisk, "SERIAL", "OTHER-SERIAL", 1),
		"capacity":    strings.Replace(allowedDisk, "16000000000", "32000000000", 1),
		"model":       strings.Replace(allowedDisk, "USB Flash", "Replacement", 1),
		"disk number": strings.Replace(allowedDisk, `"Number":4`, `"Number":5`, 1),
	}
	operations := map[string]func(*Backend, device.Device) error{
		"Unmount":     func(b *Backend, d device.Device) error { return b.Unmount(context.Background(), d) },
		"OpenWriter":  func(b *Backend, d device.Device) error { _, err := b.OpenWriter(context.Background(), d); return err },
		"FormatFAT32": func(b *Backend, d device.Device) error { return b.FormatFAT32(context.Background(), d, "TEST", nil) },
	}
	for change, changed := range changes {
		for operation, run := range operations {
			t.Run(change+"/"+operation, func(t *testing.T) {
				runner := &fakeRunner{results: []runnerResult{{output: []byte(allowedDisk)}, {output: []byte(changed)}}}
				backend := &Backend{runner: runner}
				selected, err := backend.ListAllowedDevices(context.Background())
				if err != nil || len(selected) != 1 {
					t.Fatalf("select device: %v, %v", selected, err)
				}
				if err := run(backend, selected[0]); !errors.Is(err, ErrDeviceChanged) {
					t.Fatalf("error = %v, want ErrDeviceChanged", err)
				}
				if len(runner.scripts) != 2 {
					t.Fatalf("commands = %v; destructive command was executed", runner.scripts)
				}
			})
		}
	}
}

func TestListAllowedDevicesFailsClosed(t *testing.T) {
	rejected := `[
{"Number":0,"UniqueId":"system","Size":1,"BusType":"USB","IsRemovable":true,"IsSystem":true},
{"Number":1,"UniqueId":"boot","Size":1,"BusType":"USB","IsRemovable":true,"IsBoot":true},
{"Number":2,"UniqueId":"internal","Size":1,"BusType":"NVMe","IsRemovable":false},
{"Number":3,"UniqueId":"fixed","Size":1,"BusType":"USB","IsRemovable":false},
{"Number":4,"UniqueId":"sata","Size":1,"BusType":"SATA","IsRemovable":true},
{"Number":5,"Size":1,"BusType":"USB","IsRemovable":true}]`
	tests := []struct {
		name    string
		result  runnerResult
		wantErr bool
	}{
		{name: "unsafe devices", result: runnerResult{output: []byte(rejected)}},
		{name: "malformed JSON", result: runnerResult{output: []byte(`{bad`)}, wantErr: true},
		{name: "enumeration failure", result: runnerResult{output: []byte("denied"), err: errors.New("exit")}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			devices, err := (&Backend{runner: &fakeRunner{results: []runnerResult{tt.result}}}).ListAllowedDevices(context.Background())
			if (err != nil) != tt.wantErr || len(devices) != 0 {
				t.Fatalf("devices=%v err=%v", devices, err)
			}
		})
	}
}

func TestCommandErrorsPreserveCauseAndOutput(t *testing.T) {
	cause := errors.New("command failed")
	tests := []struct {
		name   string
		run    func(*Backend, device.Device) error
		prefix string
	}{
		{name: "offline", run: func(b *Backend, d device.Device) error { return b.Unmount(context.Background(), d) }, prefix: "could not take disk offline"},
		{name: "format", run: func(b *Backend, d device.Device) error { return b.FormatFAT32(context.Background(), d, "LABEL", nil) }, prefix: "format FAT32"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{results: []runnerResult{{output: []byte(allowedDisk)}, {output: []byte(allowedDisk)}, {output: []byte("command output"), err: cause}}}
			backend := &Backend{runner: runner}
			devices, _ := backend.ListAllowedDevices(context.Background())
			err := tt.run(backend, devices[0])
			assertCommandError(t, err, cause, tt.prefix, "command output")
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
	changed := strings.Replace(allowedDisk, "16000000000", "1", 1)
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{results: []runnerResult{{output: []byte(allowedDisk)}, {output: []byte(changed)}}}
			backend := &Backend{runner: runner}
			devices, _ := backend.ListAllowedDevices(context.Background())
			if err := operation(backend, devices[0]); !errors.Is(err, ErrDeviceChanged) {
				t.Fatalf("error = %v", err)
			}
			if len(runner.scripts) != 2 || runner.scripts[1] != listScript {
				t.Fatalf("commands = %v", runner.scripts)
			}
		})
	}
}

func TestFormatLabelCannotChangePowerShellStructure(t *testing.T) {
	runner := &fakeRunner{output: []byte(allowedDisk)}
	backend := &Backend{runner: runner}
	devices, _ := backend.ListAllowedDevices(context.Background())
	if err := backend.FormatFAT32(context.Background(), devices[0], `A'; Clear-Disk -Number 0; 'B`, nil); err != nil {
		t.Fatal(err)
	}
	format := runner.scripts[len(runner.scripts)-1]
	if strings.Count(format, "Clear-Disk") != 2 || !strings.Contains(format, `A''; Clear-Disk -Number 0; ''B`) {
		t.Fatalf("label was not kept inside the quoted literal: %s", format)
	}
}

func TestUnmountRevalidatesAndTakesDiskOffline(t *testing.T) {
	online := `[{"Number":4,"FriendlyName":"USB Flash","SerialNumber":"SERIAL","UniqueId":"USB-ID","Size":16000000000,"BusType":"USB","IsRemovable":true,"IsOffline":false}]`
	offline := strings.Replace(online, `"IsOffline":false`, `"IsOffline":true`, 1)
	runner := &fakeRunner{results: []runnerResult{{output: []byte(online)}, {output: []byte(online)}, {}, {output: []byte(offline)}}}
	backend := &Backend{runner: runner}
	devices, err := backend.ListAllowedDevices(context.Background())
	if err != nil || len(devices) != 1 {
		t.Fatalf("ListAllowedDevices() = %v, %v", devices, err)
	}
	if err := backend.Unmount(context.Background(), devices[0]); err != nil {
		t.Fatal(err)
	}
	if len(runner.scripts) != 4 {
		t.Fatalf("PowerShell scripts = %q, want select, revalidate, offline, verify", runner.scripts)
	}
	if !strings.Contains(runner.scripts[2], "Set-Disk -Number 4 -IsOffline $true") {
		t.Fatalf("offline PowerShell script = %q", runner.scripts[2])
	}
}

func TestUnmountFailsIfDiskRemainsOnline(t *testing.T) {
	runner := &fakeRunner{output: []byte(allowedDisk)}
	backend := &Backend{runner: runner}
	devices, err := backend.ListAllowedDevices(context.Background())
	if err != nil || len(devices) != 1 {
		t.Fatalf("ListAllowedDevices() = %v, %v", devices, err)
	}
	if err := backend.Unmount(context.Background(), devices[0]); !errors.Is(err, ErrUnmountFailed) {
		t.Fatalf("Unmount() error = %v, want %v", err, ErrUnmountFailed)
	}
}

func TestFormatFAT32RevalidatesAndCreatesMBRVolume(t *testing.T) {
	runner := &fakeRunner{output: []byte(`[{"Number":4,"FriendlyName":"USB Flash","SerialNumber":"SERIAL","UniqueId":"USB-ID","Size":16000000000,"BusType":"USB","IsRemovable":true}]`)}
	backend := &Backend{runner: runner}
	devices, err := backend.ListAllowedDevices(context.Background())
	if err != nil || len(devices) != 1 {
		t.Fatalf("ListAllowedDevices() = %v, %v", devices, err)
	}
	if err := backend.FormatFAT32(context.Background(), devices[0], "GO'FLASHER", nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Clear-Disk -Number 4", "Initialize-Disk -Number 4 -PartitionStyle MBR", "Format-Volume", "-FileSystem FAT32", "GO''FLASHER"} {
		if !strings.Contains(runner.script, want) {
			t.Fatalf("format script %q does not contain %q", runner.script, want)
		}
	}
}

func TestBackendCommandsAlwaysHaveDeadlines(t *testing.T) {
	runner := &fakeRunner{output: []byte(allowedDisk)}
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
