package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/image"
	"github.com/goflasher/goflasher/internal/installer"
)

type installerWorkflowBackend struct {
	target                                        device.Device
	unmounted, opened, flushed, released, ejected int
	openErr                                       error
}

type availableInstallerSplitter struct{ installer.WIMSplitter }

func (availableInstallerSplitter) Preflight(context.Context) error { return nil }

func (b *installerWorkflowBackend) ListAllowedDevices(context.Context) ([]device.Device, error) {
	return []device.Device{b.target}, nil
}
func (b *installerWorkflowBackend) RefreshDevice(context.Context, string) (device.Device, error) {
	return b.target, nil
}
func (b *installerWorkflowBackend) Unmount(context.Context, device.Device) error {
	b.unmounted++
	return nil
}
func (b *installerWorkflowBackend) OpenWriter(context.Context, device.Device) (io.WriteCloser, error) {
	t, err := os.OpenFile(b.target.Path, os.O_WRONLY, 0)
	return t, err
}
func (b *installerWorkflowBackend) OpenReader(context.Context, device.Device) (io.ReadCloser, error) {
	return os.Open(b.target.Path)
}
func (b *installerWorkflowBackend) Flush(context.Context, device.Device) error {
	b.flushed++
	return nil
}
func (b *installerWorkflowBackend) Eject(context.Context, device.Device) error {
	b.ejected++
	return nil
}
func (b *installerWorkflowBackend) ReleaseDevice(device.Device) error { b.released++; return nil }
func (b *installerWorkflowBackend) OpenInstallerTarget(context.Context, device.Device) (InstallerTarget, error) {
	b.opened++
	if b.openErr != nil {
		return nil, b.openErr
	}
	return os.OpenFile(b.target.Path, os.O_RDWR, 0)
}
func (b *installerWorkflowBackend) OpenInstallerReader(context.Context, device.Device) (InstallerReader, error) {
	return os.Open(b.target.Path)
}

func TestServiceUsesWindowsExecutorAndSemanticResultFields(t *testing.T) {
	fixture := newWindowsExecutorFixture(t)
	result, err := fixture.service().Run(context.Background(), fixture.info, fixture.target, RunOptions{Eject: true}, nil)
	assertNoError(t, err)
	assertEqual(t, "plan kind", result.PlanKind, PlanWindowsInstaller)
	assertPositive(t, "files written", result.FilesWritten)
	assertNonempty(t, "manifest SHA-256", result.ManifestSHA256)
	assertEqual(t, "semantic verification", result.SemanticVerified, true)
	assertEqual(t, "source SHA-256", result.SourceSHA256, "")
	assertEqual(t, "target SHA-256", result.TargetSHA256, "")
	assertEqual(t, "backend lifecycle", fixture.backend.lifecycle(), [5]int{1, 1, 1, 1, 1})
	assertEqual(t, "state", fixture.state.State(), Completed)
}

func TestWindowsExecutorFailureUsesSharedReleaseAndFailedState(t *testing.T) {
	fixture := newWindowsExecutorFixture(t)
	openErr := errors.New("exclusive open failed")
	fixture.backend.openErr = openErr
	_, err := fixture.service().Run(context.Background(), fixture.info, fixture.target, RunOptions{}, nil)
	assertErrorIs(t, err, openErr)
	assertEqual(t, "backend lifecycle", fixture.backend.lifecycle(), [5]int{1, 1, 0, 1, 0})
	assertEqual(t, "state", fixture.state.State(), Failed)
}

type windowsExecutorFixture struct {
	info    image.Info
	target  device.Device
	backend *installerWorkflowBackend
	state   *StateMachine
}

func newWindowsExecutorFixture(t *testing.T) windowsExecutorFixture {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "windows.iso")
	targetPath := filepath.Join(dir, "target")
	if err := os.WriteFile(source, windowsISOFixture(), 0600); err != nil {
		t.Fatal(err)
	}
	const targetSize = 80 << 20
	if err := os.WriteFile(targetPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(targetPath, targetSize); err != nil {
		t.Fatal(err)
	}
	info, err := image.Detect(source)
	if err != nil {
		t.Fatal(err)
	}
	target := device.Device{ID: "installer", Path: targetPath, Size: targetSize, IsAllowed: true}
	return windowsExecutorFixture{info: info, target: target, backend: &installerWorkflowBackend{target: target}, state: readyToRunState(t)}
}

func (f windowsExecutorFixture) service() *Service {
	return &Service{Backend: f.backend, State: f.state, InstallerSplitter: availableInstallerSplitter{}}
}

func (b *installerWorkflowBackend) lifecycle() [5]int {
	return [5]int{b.unmounted, b.opened, b.flushed, b.released, b.ejected}
}

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func assertErrorIs(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

func assertEqual[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func assertPositive(t *testing.T, name string, got int) {
	t.Helper()
	if got <= 0 {
		t.Fatalf("%s = %d", name, got)
	}
}

func assertNonempty(t *testing.T, name, got string) {
	t.Helper()
	if got == "" {
		t.Fatalf("%s is empty", name)
	}
}
