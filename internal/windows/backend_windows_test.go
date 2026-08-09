//go:build windows

package windows

import (
	"context"
	"strings"
	"testing"
)

type fakeRunner struct {
	output []byte
	err    error
	script string
}

func (f *fakeRunner) Output(_ context.Context, script string) ([]byte, error) {
	f.script = script
	return f.output, f.err
}

func TestListAllowedDevices(t *testing.T) {
	runner := &fakeRunner{output: []byte(`[
{"Number":4,"FriendlyName":"USB Flash","SerialNumber":"SERIAL","UniqueId":"USB-ID","Size":16000000000,"BusType":"USB","IsRemovable":true,"PartitionCount":1,"HasDriveLetter":true},
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
	got := devices[0]
	if got.ID != "USB-ID" || got.Path != `\\.\PhysicalDrive4` || !got.Mounted || !got.IsAllowed {
		t.Fatalf("unexpected allowed device: %+v", got)
	}
}

func TestUnmountRevalidatesAndTakesDiskOffline(t *testing.T) {
	runner := &fakeRunner{output: []byte(`[{"Number":4,"FriendlyName":"USB Flash","SerialNumber":"SERIAL","UniqueId":"USB-ID","Size":16000000000,"BusType":"USB","IsRemovable":true,"HasDriveLetter":true}]`)}
	backend := &Backend{runner: runner}
	devices, err := backend.ListAllowedDevices(context.Background())
	if err != nil || len(devices) != 1 {
		t.Fatalf("ListAllowedDevices() = %v, %v", devices, err)
	}
	if err := backend.Unmount(context.Background(), devices[0]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.script, "Set-Disk -Number 4 -IsOffline $true") {
		t.Fatalf("unexpected PowerShell script: %q", runner.script)
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
