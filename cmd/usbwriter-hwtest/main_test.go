package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goflasher/goflasher/internal/device"
)

func TestAllowlistValidationAndExactSelection(t *testing.T) {
	approved := allowedDevice{
		Identity: "usb-serial", Serial: "serial", Capacity: 4096,
		Model: "Disposable USB", Disposable: true,
	}
	list := allowlist{Version: specificationVersion, Devices: []allowedDevice{approved}}
	if !list.hasSupportedVersion() {
		t.Fatal("valid v1 allowlist was rejected")
	}
	if !approved.isValid(map[string]bool{}) {
		t.Fatal("valid allowlist entry was rejected")
	}
	if (allowedDevice{}).isValid(map[string]bool{}) {
		t.Fatal("empty allowlist entry was accepted")
	}
	if approved.isValid(map[string]bool{approved.Identity: true}) {
		t.Fatal("duplicate identity was accepted")
	}

	got := approvedDevice(list, approved.Identity)
	if got != approved {
		t.Fatalf("approvedDevice() = %+v, want %+v", got, approved)
	}
	matching := device.Device{ID: approved.Identity, Serial: approved.Serial, Size: approved.Capacity, Model: approved.Model}
	if selected, ok := exactDevice([]device.Device{matching}, approved); !ok || selected.ID != matching.ID {
		t.Fatalf("exactDevice() = %+v, %v", selected, ok)
	}
	mismatch := matching
	mismatch.Size++
	if _, ok := exactDevice([]device.Device{mismatch}, approved); ok {
		t.Fatal("device with mismatched capacity was selected")
	}
	if !matchesAllowedDevice(matching, approved) {
		t.Fatal("matching device metadata was rejected")
	}
	for name, changed := range map[string]device.Device{
		"identity": {ID: "other", Serial: matching.Serial, Size: matching.Size, Model: matching.Model},
		"serial":   {ID: matching.ID, Serial: "other", Size: matching.Size, Model: matching.Model},
		"capacity": {ID: matching.ID, Serial: matching.Serial, Size: matching.Size + 1, Model: matching.Model},
		"model":    {ID: matching.ID, Serial: matching.Serial, Size: matching.Size, Model: "other"},
	} {
		t.Run("rejects mismatched "+name, func(t *testing.T) {
			if matchesAllowedDevice(changed, approved) {
				t.Fatalf("mismatched %s was accepted: %+v", name, changed)
			}
		})
	}
	withoutSerial := approved
	withoutSerial.Serial = ""
	if !matchesAllowedDevice(matching, withoutSerial) {
		t.Fatal("optional serial constrained an otherwise matching device")
	}
	if got := approvedOrEmpty(list, "unknown"); got != (allowedDevice{}) {
		t.Fatalf("approvedOrEmpty() = %+v, want empty entry", got)
	}
}

func TestReadAllowlist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allowlist.json")
	data := `{"version":"goflasher-hwtest/v1","devices":[{"identity":"id","capacity":1024,"model":"USB","disposable":true}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	got := readAllowlist(path)
	if got.Version != specificationVersion || len(got.Devices) != 1 || got.Devices[0].Identity != "id" {
		t.Fatalf("readAllowlist() = %+v", got)
	}
}

func TestChallengeValidityAndConsumption(t *testing.T) {
	now := time.Now()
	c := challenge{Version: specificationVersion, Identity: "id", Nonce: "nonce", Created: now.Add(-time.Minute)}
	answer := "ERASE id nonce"
	if !c.isValid("id", answer, now) {
		t.Fatal("fresh matching challenge was rejected")
	}
	for name, changed := range map[string]challenge{
		"wrong version":  {Version: "v0", Identity: "id", Nonce: "nonce", Created: c.Created},
		"wrong identity": {Version: specificationVersion, Identity: "other", Nonce: "nonce", Created: c.Created},
		"expired":        {Version: specificationVersion, Identity: "id", Nonce: "nonce", Created: now.Add(-16 * time.Minute)},
		"future":         {Version: specificationVersion, Identity: "id", Nonce: "nonce", Created: now.Add(time.Minute)},
	} {
		t.Run(name, func(t *testing.T) {
			if changed.isValid("id", answer, now) {
				t.Fatal("invalid challenge was accepted")
			}
		})
	}
	if c.isValid("id", "ERASE id incorrect", now) {
		t.Fatal("incorrect confirmation was accepted")
	}

	path := filepath.Join(t.TempDir(), "challenge.json")
	prepareChallenge(path, "id")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var prepared challenge
	if err := json.Unmarshal(data, &prepared); err != nil {
		t.Fatal(err)
	}
	if len(prepared.Nonce) != 32 {
		t.Fatalf("challenge nonce = %q, want 32 hexadecimal characters", prepared.Nonce)
	}
	consumeChallenge(path, "id", "ERASE id "+prepared.Nonce)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed challenge still exists: %v", err)
	}
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
