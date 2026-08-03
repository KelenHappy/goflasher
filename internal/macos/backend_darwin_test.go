//go:build darwin

package macos

import (
	"context"
	"fmt"
	"testing"
)

type fakeRunner struct {
	json map[string][]byte
	runs [][]string
}

func (f *fakeRunner) JSON(_ context.Context, args ...string) ([]byte, error) {
	out, ok := f.json[fmt.Sprint(args)]
	if !ok {
		return nil, fmt.Errorf("unexpected JSON command: %v", args)
	}
	return out, nil
}
func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.runs = append(f.runs, append([]string(nil), args...))
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
	got := devices[0]
	if got.ID != "IOService:/USB/disk@1" || got.Path != "/dev/rdisk4" || !got.Mounted || !got.IsAllowed {
		t.Fatalf("unexpected device: %+v", got)
	}
}

func TestDevicePathConversions(t *testing.T) {
	if got := rawDevice("/dev/disk12"); got != "/dev/rdisk12" {
		t.Fatalf("rawDevice() = %q", got)
	}
	if got := wholeDevice("/dev/rdisk12"); got != "/dev/disk12" {
		t.Fatalf("wholeDevice() = %q", got)
	}
}
