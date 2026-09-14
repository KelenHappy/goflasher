package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/goflasher/goflasher/internal/device"
)

func testAllowedDevice() allowedDevice {
	return allowedDevice{
		Identity: "usb-serial", Serial: "serial", Capacity: 4096,
		Model: "Disposable USB", Disposable: true,
	}
}

func deviceFor(a allowedDevice) device.Device {
	return device.Device{ID: a.Identity, Serial: a.Serial, Size: a.Capacity, Model: a.Model}
}

func TestAllowlistEntryValidation(t *testing.T) {
	approved := testAllowedDevice()
	list := allowlist{Version: specificationVersion, Devices: []allowedDevice{approved}}
	if !list.hasSupportedVersion() {
		t.Fatal("valid v1 allowlist was rejected")
	}
	tests := []struct {
		name  string
		entry allowedDevice
		seen  map[string]bool
		want  bool
	}{
		{"valid entry", approved, map[string]bool{}, true},
		{"empty entry", allowedDevice{}, map[string]bool{}, false},
		{"duplicate identity", approved, map[string]bool{approved.Identity: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.entry.isValid(tt.seen); got != tt.want {
				t.Fatalf("isValid() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestApprovedDeviceLookup(t *testing.T) {
	approved := testAllowedDevice()
	list := allowlist{Version: specificationVersion, Devices: []allowedDevice{approved}}
	if got := approvedDevice(list, approved.Identity); got != approved {
		t.Fatalf("approvedDevice() = %+v, want %+v", got, approved)
	}
	if got := approvedOrEmpty(list, "unknown"); got != (allowedDevice{}) {
		t.Fatalf("approvedOrEmpty() = %+v, want empty entry", got)
	}
}

func TestExactDeviceSelection(t *testing.T) {
	approved := testAllowedDevice()
	matching := deviceFor(approved)
	if selected, ok := exactDevice([]device.Device{matching}, approved); !ok || selected.ID != matching.ID {
		t.Fatalf("exactDevice() = %+v, %v", selected, ok)
	}
	mismatch := matching
	mismatch.Size++
	if _, ok := exactDevice([]device.Device{mismatch}, approved); ok {
		t.Fatal("device with mismatched capacity was selected")
	}
}

func TestMatchesAllowedDevice(t *testing.T) {
	approved := testAllowedDevice()
	withoutSerial := approved
	withoutSerial.Serial = ""
	matching := deviceFor(approved)
	changed := func(change func(*device.Device)) device.Device {
		d := matching
		change(&d)
		return d
	}
	tests := []struct {
		name    string
		device  device.Device
		allowed allowedDevice
		want    bool
	}{
		{"accepts matching metadata", matching, approved, true},
		{"accepts missing optional serial", matching, withoutSerial, true},
		{"rejects mismatched identity", changed(func(d *device.Device) { d.ID = "other" }), approved, false},
		{"rejects mismatched serial", changed(func(d *device.Device) { d.Serial = "other" }), approved, false},
		{"rejects mismatched capacity", changed(func(d *device.Device) { d.Size++ }), approved, false},
		{"rejects mismatched model", changed(func(d *device.Device) { d.Model = "other" }), approved, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesAllowedDevice(tt.device, tt.allowed); got != tt.want {
				t.Fatalf("matchesAllowedDevice(%+v) = %t, want %t", tt.device, got, tt.want)
			}
		})
	}
}

func TestReadAllowlist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allowlist.json")
	data := `{"version":"goflasher-hwtest/v1","devices":[{"identity":"id","capacity":1024,"model":"USB","disposable":true}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	want := allowlist{
		Version: specificationVersion,
		Devices: []allowedDevice{{Identity: "id", Capacity: 1024, Model: "USB", Disposable: true}},
	}
	if got := readAllowlist(path); !reflect.DeepEqual(got, want) {
		t.Fatalf("readAllowlist() = %+v, want %+v", got, want)
	}
}

func TestChallengeValidity(t *testing.T) {
	now := time.Now()
	valid := challenge{Version: specificationVersion, Identity: "id", Nonce: "nonce", Created: now.Add(-time.Minute)}
	const answer = "ERASE id nonce"
	changed := func(change func(*challenge)) challenge {
		c := valid
		change(&c)
		return c
	}
	tests := []struct {
		name      string
		challenge challenge
		answer    string
		want      bool
	}{
		{"fresh matching challenge", valid, answer, true},
		{"wrong version", changed(func(c *challenge) { c.Version = "v0" }), answer, false},
		{"wrong identity", changed(func(c *challenge) { c.Identity = "other" }), answer, false},
		{"expired", changed(func(c *challenge) { c.Created = now.Add(-16 * time.Minute) }), answer, false},
		{"future", changed(func(c *challenge) { c.Created = now.Add(time.Minute) }), answer, false},
		{"incorrect confirmation", valid, "ERASE id incorrect", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.challenge.isValid("id", tt.answer, now); got != tt.want {
				t.Fatalf("isValid() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestChallengeConsumption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "challenge.json")
	prepareChallenge(path, "id")
	prepared := readChallengeFile(t, path)
	if len(prepared.Nonce) != 32 {
		t.Fatalf("challenge nonce = %q, want 32 hexadecimal characters", prepared.Nonce)
	}
	consumeChallenge(path, "id", "ERASE id "+prepared.Nonce)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed challenge still exists: %v", err)
	}
}

func readChallengeFile(t *testing.T, path string) challenge {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c challenge
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSnapshotAndAddressReuseHelpers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	original := device.Device{ID: "first", Path: "/dev/sdb", Major: 8, Minor: 16, Size: 4096}
	checkOrWriteSnapshot(path, original)
	checkOrWriteSnapshot(path, original)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	checkSnapshot(data, original)

	s := snapshot{Version: specificationVersion, Identity: original.ID, Path: original.Path, Major: original.Major, Minor: original.Minor, Capacity: original.Size}
	if !s.matches(original) {
		t.Fatal("snapshot did not match its source device")
	}
	changedCapacity := original
	changedCapacity.Size++
	if s.matches(changedCapacity) {
		t.Fatal("snapshot matched a device with different capacity")
	}
	if addressReusedBy(original, s) {
		t.Fatal("same identity was considered address reuse")
	}
	replacement := device.Device{ID: "second", Path: original.Path, Major: 8, Minor: 32, Size: 4096, Model: "USB"}
	if !addressReusedBy(replacement, s) {
		t.Fatal("different identity on the same path was not considered reuse")
	}
	replacement.Path = "/dev/sdc"
	replacement.Major, replacement.Minor = original.Major, original.Minor
	if !sameDiskNumber(replacement, s) || !addressReusedBy(replacement, s) {
		t.Fatal("different identity on the same device number was not considered reuse")
	}
}

func TestCancellationClassification(t *testing.T) {
	if isCancellation(nil) {
		t.Fatal("nil error classified as cancellation")
	}
	if !isCancellation(context.Canceled) || !isCancellation(errors.New("operation CANCELLED by operator")) {
		t.Fatal("cancellation error was not recognized")
	}
	if isCancellation(errors.New("write failed")) {
		t.Fatal("ordinary error classified as cancellation")
	}
}
