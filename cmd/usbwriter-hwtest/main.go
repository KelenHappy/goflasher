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

type commandOptions struct {
	allowlistPath, deviceID, imagePath, testCase string
	confirmation, challengePath, snapshotPath    string
	cancelAfter                                  time.Duration
}

type deviceSelection struct {
	device device.Device
	found  bool
}

func parseFlags() commandOptions {
	var options commandOptions
	flag.StringVar(&options.allowlistPath, "allowlist", "", "reviewed v1 JSON device allowlist (required)")
	flag.StringVar(&options.deviceID, "device-id", "", "exact allowlisted stable identity (required; never inferred)")
	flag.StringVar(&options.imagePath, "image", "", "versioned disposable test image")
	flag.StringVar(&options.testCase, "case", "inventory", "inventory|prepare|enumerate-present|enumerate-absent|path-reuse|write-verify-eject|write-cancel|write-remove|corruption-detect")
	flag.StringVar(&options.confirmation, "confirmation", "", "one-time confirmation emitted by --case prepare")
	flag.StringVar(&options.challengePath, "challenge-file", ".goflasher-hwtest-challenge.json", "one-time challenge state")
	flag.StringVar(&options.snapshotPath, "snapshot", ".goflasher-hwtest-snapshot.json", "enumeration/reuse snapshot")
	flag.DurationVar(&options.cancelAfter, "cancel-after", 2*time.Second, "write-cancel deadline")
	flag.Parse()
	return options
}

func main() {
	options := parseFlags()
	if options.allowlistPath == "" {
		fatal(errors.New("--allowlist is required; implicit device selection is forbidden"))
	}
	approved := readAllowlist(options.allowlistPath)
	backend := newBackend()
	ctx := context.Background()
	devices, err := backend.ListAllowedDevices(ctx)
	fatal(err)
	printInventory(devices, approved)
	if options.testCase == "inventory" {
		return
	}
	if options.deviceID == "" {
		fatal(errors.New("--device-id is required; the first enumerated device is never selected"))
	}
	wanted := approvedDevice(approved, options.deviceID)
	if !wanted.Disposable {
		fatal(errors.New("allowlist entry is not explicitly marked disposable"))
	}
	selected, found := exactDevice(devices, wanted)
	selection := deviceSelection{device: selected, found: found}
	if runEnumerationCase(options, devices, approved, selection) {
		return
	}
	runDestructiveCase(ctx, backend, options, selection)
}

func runEnumerationCase(options commandOptions, devices []device.Device, approved allowlist, selection deviceSelection) bool {
	switch options.testCase {
	case "prepare":
		if !selection.found {
			fatal(errors.New("exact allowlisted device is not currently allowed"))
		}
		prepareChallenge(options.challengePath, selection.device.ID)
	case "enumerate-absent":
		if selection.found {
			fatal(errors.New("device remained present after removal"))
		}
		fmt.Println("PASS: allowlisted identity is absent")
	case "enumerate-present":
		if !selection.found {
			fatal(errors.New("allowlisted identity was not re-enumerated"))
		}
		checkOrWriteSnapshot(options.snapshotPath, selection.device)
		fmt.Println("PASS: exact identity present and snapshot recorded")
	case "path-reuse":
		checkPathReuse(options.snapshotPath, devices, approved, options.deviceID)
	default:
		return false
	}
	return true
}

func runDestructiveCase(ctx context.Context, backend device.Backend, options commandOptions, selection deviceSelection) {
	if !selection.found {
		fatal(errors.New("exact allowlisted device is not currently allowed"))
	}
	selected := selection.device
	consumeChallenge(options.challengePath, selected.ID, options.confirmation)
	if options.imagePath == "" {
		fatal(errors.New("--image is required for destructive cases"))
	}
	info, err := image.Detect(options.imagePath)
	fatal(err)
	info, err = image.Inspect(info)
	fatal(err)
	if info.UncompressedSize > selected.Size {
		fatal(errors.New("test image exceeds disposable device"))
	}

	switch options.testCase {
	case "write-verify-eject":
		result, err := run(ctx, writeRequest{backend: backend, image: info, selected: selected, options: app.RunOptions{Verify: true, Eject: true}})
		fatal(err)
		if !result.Verified || !result.Ejected {
			fatal(errors.New("verification or eject result missing"))
		}
		fmt.Printf("PASS: bytes=%d sha256=%s verified=%v ejected=%v\n", result.BytesWritten, result.TargetSHA256, result.Verified, result.Ejected)
	case "write-cancel":
		cancelCtx, cancel := context.WithTimeout(ctx, options.cancelAfter)
		defer cancel()
		_, err := run(cancelCtx, writeRequest{backend: backend, image: info, selected: selected})
		if !isCancellation(err) {
			fatal(fmt.Errorf("expected cancellation, got %v", err))
		}
		fmt.Printf("PASS: cancellation rejected completion: %v\n", err)
	case "write-remove":
		fmt.Fprintln(os.Stderr, "REMOVE THE DISPOSABLE DEVICE WHILE WRITING; success is a test failure")
		_, err := run(ctx, writeRequest{backend: backend, image: info, selected: selected})
		if err == nil {
			fatal(errors.New("write completed; removal was not observed"))
		}
		fmt.Printf("PASS: removal failed closed: %v\n", err)
	case "corruption-detect":
		_, err := run(ctx, writeRequest{backend: backend, image: info, selected: selected, options: app.RunOptions{Verify: true}})
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
		fatal(fmt.Errorf("unknown --case %q", options.testCase))
	}
}

func isCancellation(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || strings.Contains(strings.ToLower(err.Error()), "cancel")
}

func readAllowlist(path string) allowlist {
	b, err := os.ReadFile(path)
	fatal(err)
	var a allowlist
	fatal(json.Unmarshal(b, &a))
	if !a.hasSupportedVersion() {
		fatal(errors.New("allowlist must be non-empty goflasher-hwtest/v1"))
	}
	seen := map[string]bool{}
	for _, d := range a.Devices {
		if !d.isValid(seen) {
			fatal(errors.New("allowlist entries require unique identity, model, and capacity"))
		}
		seen[d.Identity] = true
	}
	return a
}
func (a allowlist) hasSupportedVersion() bool {
	return a.Version == specificationVersion && len(a.Devices) > 0
}
func (d allowedDevice) isValid(seen map[string]bool) bool {
	if d.Identity == "" || d.Capacity == 0 {
		return false
	}
	if d.Model == "" || seen[d.Identity] {
		return false
	}
	return true
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
		if matchesAllowedDevice(d, a) {
			return d, true
		}
	}
	return device.Device{}, false
}
func matchesAllowedDevice(d device.Device, allowed allowedDevice) bool {
	return d.ID == allowed.Identity &&
		d.Size == allowed.Capacity &&
		d.Model == allowed.Model &&
		matchesOptionalSerial(d.Serial, allowed.Serial)
}
func matchesOptionalSerial(actual, allowed string) bool {
	return allowed == "" || actual == allowed
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
	if !c.isValid(id, answer, time.Now()) {
		fatal(errors.New("invalid, expired, or already-consumed confirmation"))
	}
}
func (c challenge) isValid(id, answer string, now time.Time) bool {
	age := now.Sub(c.Created)
	if c.Version != specificationVersion || c.Identity != id {
		return false
	}
	if age < 0 || age > 15*time.Minute {
		return false
	}
	return answer == "ERASE "+id+" "+c.Nonce
}
func checkOrWriteSnapshot(path string, d device.Device) {
	if existing, err := os.ReadFile(path); err == nil {
		checkSnapshot(existing, d)
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
func checkSnapshot(data []byte, d device.Device) {
	var s snapshot
	fatal(json.Unmarshal(data, &s))
	if !s.matches(d) {
		fatal(errors.New("existing snapshot is for a different device or specification"))
	}
}
func (s snapshot) matches(d device.Device) bool {
	return s.Version == specificationVersion && s.Identity == d.ID && s.Capacity == d.Size
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
		if addressReusedBy(d, s) {
			checkReplacement(d, approved)
			fmt.Printf("PASS: reused address rejected old=%q new=%q address=%q %d:%d\n", s.Identity, d.ID, d.Path, d.Major, d.Minor)
			return
		}
	}
	fatal(errors.New("no different identity observed reusing the recorded path/disk number"))
}
func addressReusedBy(d device.Device, s snapshot) bool {
	if d.ID == s.Identity {
		return false
	}
	return d.Path == s.Path || sameDiskNumber(d, s)
}
func sameDiskNumber(d device.Device, s snapshot) bool {
	return d.Major == s.Major && d.Minor == s.Minor
}
func checkReplacement(d device.Device, approved allowlist) {
	candidate := approvedOrEmpty(approved, d.ID)
	if candidate.Identity == "" {
		fatal(errors.New("reused address belongs to a device outside the reviewed allowlist"))
	}
	if _, ok := exactDevice([]device.Device{d}, candidate); !ok {
		fatal(errors.New("replacement does not match its reviewed allowlist metadata"))
	}
}

type writeRequest struct {
	backend  device.Backend
	image    image.Info
	selected device.Device
	options  app.RunOptions
}

func run(ctx context.Context, request writeRequest) (app.RunResult, error) {
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
	result, err := (&app.Service{Backend: request.backend, State: states}).Run(ctx, request.image, request.selected, request.options, updates)
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
