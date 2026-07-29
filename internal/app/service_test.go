package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/image"
)

type fileBackend struct {
	path                        string
	d                           device.Device
	unmounted, flushed, ejected bool
}

func (f *fileBackend) ListAllowedDevices(context.Context) ([]device.Device, error) {
	return []device.Device{f.d}, nil
}
func (f *fileBackend) RefreshDevice(context.Context, string) (device.Device, error) { return f.d, nil }
func (f *fileBackend) Unmount(context.Context, device.Device) error                 { f.unmounted = true; return nil }
func (f *fileBackend) OpenWriter(context.Context, device.Device) (io.WriteCloser, error) {
	return os.OpenFile(f.path, os.O_WRONLY|os.O_TRUNC, 0600)
}
func (f *fileBackend) OpenReader(context.Context, device.Device) (io.ReadCloser, error) {
	return os.Open(f.path)
}
func (f *fileBackend) Flush(context.Context, device.Device) error { f.flushed = true; return nil }
func (f *fileBackend) Eject(context.Context, device.Device) error { f.ejected = true; return nil }

func TestServiceRawWriteVerifyEject(t *testing.T) {
	payload := bytes.Repeat([]byte("image"), 4096)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.img")
	target := filepath.Join(dir, "target")
	os.WriteFile(source, payload, 0600)
	os.WriteFile(target, make([]byte, len(payload)*2), 0600)
	info, err := image.Detect(source)
	if err != nil {
		t.Fatal(err)
	}
	d := device.Device{ID: "test", Path: target, Size: uint64(len(payload) * 2), IsAllowed: true}
	backend := &fileBackend{path: target, d: d}
	states := NewStateMachine()
	for _, s := range []State{ImageSelected, Ready, Confirming} {
		if err := states.Transition(s); err != nil {
			t.Fatal(err)
		}
	}
	service := Service{Backend: backend, State: states}
	result, err := service.Run(context.Background(), info, d, RunOptions{Verify: true, Eject: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || !result.Ejected || !backend.flushed || states.State() != Completed {
		t.Fatalf("result=%+v state=%s", result, states.State())
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, payload) {
		t.Fatal("written data differs")
	}
}

func TestServiceRejectsTooSmallBeforeUnmount(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "large.img")
	os.WriteFile(source, make([]byte, 32), 0600)
	info, _ := image.Detect(source)
	d := device.Device{ID: "test", Size: 16}
	backend := &fileBackend{d: d}
	states := NewStateMachine()
	states.Transition(ImageSelected)
	states.Transition(Ready)
	states.Transition(Confirming)
	_, err := (&Service{Backend: backend, State: states}).Run(context.Background(), info, d, RunOptions{}, nil)
	if err == nil || backend.unmounted {
		t.Fatalf("error=%v unmounted=%v", err, backend.unmounted)
	}
}

func TestServiceCancellationBeforeDestructiveWork(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.img")
	os.WriteFile(source, make([]byte, 1024), 0600)
	info, _ := image.Detect(source)
	d := device.Device{ID: "test", Size: 2048}
	backend := &fileBackend{d: d}
	states := NewStateMachine()
	states.Transition(ImageSelected)
	states.Transition(Ready)
	states.Transition(Confirming)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&Service{Backend: backend, State: states}).Run(ctx, info, d, RunOptions{}, nil)
	if err == nil || backend.unmounted || states.State() != Cancelled {
		t.Fatalf("error=%v unmounted=%v state=%s", err, backend.unmounted, states.State())
	}
}
