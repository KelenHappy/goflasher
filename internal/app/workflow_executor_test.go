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

type availableInstallerSplitter struct{}

func (availableInstallerSplitter) Preflight(context.Context) error { return nil }
func (availableInstallerSplitter) Split(context.Context, io.Reader, uint64, string, uint64, func(installer.SplitPart) error) error {
	return errors.New("unexpected split")
}

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
	dir := t.TempDir()
	source := filepath.Join(dir, "windows.iso")
	targetPath := filepath.Join(dir, "target")
	if err := os.WriteFile(source, windowsISOFixture(), 0600); err != nil {
		t.Fatal(err)
	}
	const targetSize = 80 << 20
	f, err := os.Create(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(targetSize); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := image.Detect(source)
	if err != nil {
		t.Fatal(err)
	}
	target := device.Device{ID: "installer", Path: targetPath, Size: targetSize, IsAllowed: true}
	backend := &installerWorkflowBackend{target: target}
	state := readyToRunState(t)
	result, err := (&Service{Backend: backend, State: state, InstallerSplitter: availableInstallerSplitter{}}).Run(context.Background(), info, target, RunOptions{Eject: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.PlanKind != PlanWindowsInstaller || result.FilesWritten == 0 || result.ManifestSHA256 == "" || !result.SemanticVerified {
		t.Fatalf("result=%+v", result)
	}
	if result.SourceSHA256 != "" || result.TargetSHA256 != "" {
		t.Fatalf("installer populated raw hashes: %+v", result)
	}
	if backend.unmounted != 1 || backend.opened != 1 || backend.flushed != 1 || backend.released != 1 || backend.ejected != 1 {
		t.Fatalf("backend lifecycle=%+v", backend)
	}
	if state.State() != Completed {
		t.Fatalf("state=%s", state.State())
	}
}

func TestWindowsExecutorFailureUsesSharedReleaseAndFailedState(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "windows.iso")
	targetPath := filepath.Join(dir, "target")
	if err := os.WriteFile(source, windowsISOFixture(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(targetPath, 80<<20); err != nil {
		t.Fatal(err)
	}
	info, err := image.Detect(source)
	if err != nil {
		t.Fatal(err)
	}
	target := device.Device{ID: "installer", Path: targetPath, Size: 80 << 20, IsAllowed: true}
	openErr := errors.New("exclusive open failed")
	backend := &installerWorkflowBackend{target: target, openErr: openErr}
	state := readyToRunState(t)
	_, err = (&Service{Backend: backend, State: state, InstallerSplitter: availableInstallerSplitter{}}).Run(context.Background(), info, target, RunOptions{}, nil)
	if !errors.Is(err, openErr) {
		t.Fatalf("error=%v", err)
	}
	if backend.unmounted != 1 || backend.released != 1 {
		t.Fatalf("lifecycle=%+v", backend)
	}
	if state.State() != Failed {
		t.Fatalf("state=%s", state.State())
	}
}
