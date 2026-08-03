// usbwriter-hwtest implements the versioned, destructive acceptance protocol
// in spec-v1.md. It is deliberately excluded from automated test execution.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/goflasher/goflasher/internal/app"
	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/image"
	"github.com/goflasher/goflasher/internal/progress"
	"github.com/goflasher/goflasher/internal/verify"
)

const specificationVersion = "goflasher-hwtest/v1"

type allowlist struct {
	Version string          `json:"version"`
	Devices []allowedDevice `json:"devices"`
}
type allowedDevice struct {
	Identity   string `json:"identity"`
	Serial     string `json:"serial,omitempty"`
	Capacity   uint64 `json:"capacity"`
	Model      string `json:"model"`
	Disposable bool   `json:"disposable"`
}
type challenge struct {
	Version, Identity, Nonce string
	Created                  time.Time
}
type snapshot struct {
	Version, Identity, Path string
	Major, Minor            uint32
	Capacity                uint64
}

func main() {
	allowlistPath := flag.String("allowlist", "", "reviewed v1 JSON device allowlist (required)")
	deviceID := flag.String("device-id", "", "exact allowlisted stable identity (required; never inferred)")
	imagePath := flag.String("image", "", "versioned disposable test image")
	testCase := flag.String("case", "inventory", "inventory|prepare|enumerate-present|enumerate-absent|path-reuse|write-verify-eject|write-cancel|write-remove|corruption-detect")
	confirmation := flag.String("confirmation", "", "one-time confirmation emitted by --case prepare")
	challengePath := flag.String("challenge-file", ".goflasher-hwtest-challenge.json", "one-time challenge state")
	snapshotPath := flag.String("snapshot", ".goflasher-hwtest-snapshot.json", "enumeration/reuse snapshot")
	cancelAfter := flag.Duration("cancel-after", 2*time.Second, "write-cancel deadline")
	flag.Parse()

	if *allowlistPath == "" {
		fatal(errors.New("--allowlist is required; implicit device selection is forbidden"))
	}
	approved := readAllowlist(*allowlistPath)
	backend := newBackend()
	ctx := context.Background()
	devices, err := backend.ListAllowedDevices(ctx)
	fatal(err)
	printInventory(devices, approved)
	if *testCase == "inventory" {
		return
	}
	if *deviceID == "" {
		fatal(errors.New("--device-id is required; the first enumerated device is never selected"))
	}
	wanted := approvedDevice(approved, *deviceID)
	if !wanted.Disposable {
		fatal(errors.New("allowlist entry is not explicitly marked disposable"))
	}
	selected, found := exactDevice(devices, wanted)

	switch *testCase {
	case "prepare":
		if !found {
			fatal(errors.New("exact allowlisted device is not currently allowed"))
		}
		prepareChallenge(*challengePath, selected.ID)
		return
	case "enumerate-absent":
		if found {
			fatal(errors.New("device remained present after removal"))
		}
		fmt.Println("PASS: allowlisted identity is absent")
		return
	case "enumerate-present":
		if !found {
			fatal(errors.New("allowlisted identity was not re-enumerated"))
		}
		checkOrWriteSnapshot(*snapshotPath, selected)
		fmt.Println("PASS: exact identity present and snapshot recorded")
		return
	case "path-reuse":
		checkPathReuse(*snapshotPath, devices, approved, *deviceID)
		return
	}
	if !found {
		fatal(errors.New("exact allowlisted device is not currently allowed"))
	}
	consumeChallenge(*challengePath, selected.ID, *confirmation)
	if *imagePath == "" {
		fatal(errors.New("--image is required for destructive cases"))
	}
	info, err := image.Detect(*imagePath)
	fatal(err)
	info, err = image.Inspect(info)
	fatal(err)
	if info.UncompressedSize > selected.Size {
		fatal(errors.New("test image exceeds disposable device"))
	}

	switch *testCase {
	case "write-verify-eject":
		result, err := run(ctx, backend, info, selected, true, true)
		fatal(err)
		if !result.Verified || !result.Ejected {
			fatal(errors.New("verification or eject result missing"))
		}
		fmt.Printf("PASS: bytes=%d sha256=%s verified=%v ejected=%v\n", result.BytesWritten, result.TargetSHA256, result.Verified, result.Ejected)
	case "write-cancel":
		cancelCtx, cancel := context.WithTimeout(ctx, *cancelAfter)
		defer cancel()
		_, err := run(cancelCtx, backend, info, selected, false, false)
		if err == nil || (!errors.Is(err, context.Canceled) && !strings.Contains(strings.ToLower(err.Error()), "cancel")) {
			fatal(fmt.Errorf("expected cancellation, got %v", err))
		}
		fmt.Printf("PASS: cancellation rejected completion: %v\n", err)
	case "write-remove":
		fmt.Fprintln(os.Stderr, "REMOVE THE DISPOSABLE DEVICE WHILE WRITING; success is a test failure")
		_, err := run(ctx, backend, info, selected, false, false)
		if err == nil {
			fatal(errors.New("write completed; removal was not observed"))
		}
		fmt.Printf("PASS: removal failed closed: %v\n", err)
	case "corruption-detect":
		_, err := run(ctx, backend, info, selected, true, false)
		fatal(err)
		w, err := backend.OpenWriter(ctx, selected)
		fatal(err)
		_, err = w.Write([]byte{0x00, 0xff, 0x47, 0x46})
		if closeErr := w.Close(); err == nil {
			err = closeErr
		}
		fatal(err)
		fatal(backend.Flush(ctx, selected))
		r, err := backend.OpenReader(ctx, selected)
		fatal(err)
		_, err = verify.ReadBack(ctx, r, info.UncompressedSize, info.SHA256, nil)
		_ = r.Close()
		if !errors.Is(err, verify.ErrMismatch) {
			fatal(fmt.Errorf("expected corruption mismatch, got %v", err))
		}
		fmt.Println("PASS: deliberate corruption detected")
	default:
		fatal(fmt.Errorf("unknown --case %q", *testCase))
	}
}

func readAllowlist(path string) allowlist {
	b, err := os.ReadFile(path)
	fatal(err)
	var a allowlist
	fatal(json.Unmarshal(b, &a))
	if a.Version != specificationVersion || len(a.Devices) == 0 {
		fatal(errors.New("allowlist must be non-empty goflasher-hwtest/v1"))
	}
	seen := map[string]bool{}
	for _, d := range a.Devices {
		if d.Identity == "" || d.Capacity == 0 || d.Model == "" || seen[d.Identity] {
			fatal(errors.New("allowlist entries require unique identity, model, and capacity"))
		}
		seen[d.Identity] = true
	}
	return a
}
func approvedDevice(a allowlist, id string) allowedDevice {
	for _, d := range a.Devices {
		if d.Identity == id {
			return d
		}
	}
	fatal(errors.New("--device-id is not in the reviewed allowlist"))
	return allowedDevice{}
}
func exactDevice(devices []device.Device, a allowedDevice) (device.Device, bool) {
	for _, d := range devices {
		if d.ID == a.Identity && d.Size == a.Capacity && (a.Serial == "" || d.Serial == a.Serial) && d.Model == a.Model {
			return d, true
		}
	}
	return device.Device{}, false
}
func printInventory(devices []device.Device, a allowlist) {
	for _, d := range devices {
		_, ok := exactDevice(devices, approvedOrEmpty(a, d.ID))
		fmt.Printf("allowed=%v id=%q path=%q number=%d:%d model=%q serial=%q capacity=%d mounted=%v\n", ok, d.ID, d.Path, d.Major, d.Minor, d.Model, d.Serial, d.Size, d.Mounted)
	}
}
func approvedOrEmpty(a allowlist, id string) allowedDevice {
	for _, d := range a.Devices {
		if d.Identity == id {
			return d
		}
	}
	return allowedDevice{}
}
func prepareChallenge(path, id string) {
	var b [16]byte
	_, err := rand.Read(b[:])
	fatal(err)
	c := challenge{Version: specificationVersion, Identity: id, Nonce: hex.EncodeToString(b[:]), Created: time.Now().UTC()}
	data, _ := json.MarshalIndent(c, "", "  ")
	fatal(os.WriteFile(path, data, 0600))
	fmt.Printf("ONE-TIME CONFIRMATION (valid 15 minutes): ERASE %s %s\n", id, c.Nonce)
}
func consumeChallenge(path, id, answer string) {
	data, err := os.ReadFile(path)
	fatal(err)
	var c challenge
	fatal(json.Unmarshal(data, &c))
	fatal(os.Remove(path))
	expected := "ERASE " + id + " " + c.Nonce
	age := time.Since(c.Created)
	if c.Version != specificationVersion || c.Identity != id || age < 0 || age > 15*time.Minute || answer != expected {
		fatal(errors.New("invalid, expired, or already-consumed confirmation"))
	}
}
func checkOrWriteSnapshot(path string, d device.Device) {
	if existing, err := os.ReadFile(path); err == nil {
		var s snapshot
		fatal(json.Unmarshal(existing, &s))
		if s.Version != specificationVersion || s.Identity != d.ID || s.Capacity != d.Size {
			fatal(errors.New("existing snapshot is for a different device or specification"))
		}
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		fatal(err)
	}
	s := snapshot{specificationVersion, d.ID, d.Path, d.Major, d.Minor, d.Size}
	b, _ := json.MarshalIndent(s, "", "  ")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	fatal(err)
	_, err = f.Write(b)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	fatal(err)
}
func checkPathReuse(path string, devices []device.Device, approved allowlist, selectedID string) {
	b, err := os.ReadFile(path)
	fatal(err)
	var s snapshot
	fatal(json.Unmarshal(b, &s))
	if s.Version != specificationVersion || s.Identity != selectedID {
		fatal(errors.New("snapshot identity/version mismatch"))
	}
	for _, d := range devices {
		if (d.Path == s.Path || (d.Major == s.Major && d.Minor == s.Minor)) && d.ID != s.Identity {
			candidate := approvedOrEmpty(approved, d.ID)
			if candidate.Identity == "" {
				fatal(errors.New("reused address belongs to a device outside the reviewed allowlist"))
			}
			if _, ok := exactDevice([]device.Device{d}, candidate); !ok {
				fatal(errors.New("replacement does not match its reviewed allowlist metadata"))
			}
			fmt.Printf("PASS: reused address rejected old=%q new=%q address=%q %d:%d\n", s.Identity, d.ID, d.Path, d.Major, d.Minor)
			return
		}
	}
	fatal(errors.New("no different identity observed reusing the recorded path/disk number"))
}
func run(ctx context.Context, backend device.Backend, info image.Info, selected device.Device, verify, eject bool) (app.RunResult, error) {
	states := app.NewStateMachine()
	for _, s := range []app.State{app.ImageSelected, app.Ready, app.Confirming} {
		fatal(states.Transition(s))
	}
	updates := make(chan progress.Update, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for u := range updates {
			fmt.Printf("stage=%s bytes=%d total=%d\n", u.Stage, u.BytesProcessed, u.TotalBytes)
		}
	}()
	result, err := (&app.Service{Backend: backend, State: states}).Run(ctx, info, selected, app.RunOptions{Verify: verify, Eject: eject}, updates)
	close(updates)
	<-done
	return result, err
}
func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}
