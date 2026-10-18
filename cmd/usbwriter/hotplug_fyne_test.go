//go:build fyne && (linux || windows || darwin)

package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goflasher/goflasher/internal/device"
)

func TestDebouncerCoalescesBursts(t *testing.T) {
	var runs atomic.Int32
	d := &debouncer{delay: 20 * time.Millisecond, fn: func() { runs.Add(1) }}
	for range 5 {
		d.trigger()
	}
	time.Sleep(100 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs after one burst = %d, want 1", got)
	}
	d.stop()
	d.trigger()
	time.Sleep(50 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs after stop = %d, want 1", got)
	}
}

// hotplugBackend reports one device change once watching starts.
type hotplugBackend struct {
	device.Backend
	scans atomic.Int32
}

func (b *hotplugBackend) ListAllowedDevices(context.Context) ([]device.Device, device.ScanReport, error) {
	b.scans.Add(1)
	return []device.Device{{Path: "/dev/new"}}, device.ScanReport{}, nil
}

func (b *hotplugBackend) WatchDevices(ctx context.Context, changed func()) error {
	changed()
	<-ctx.Done()
	return nil
}

func TestHotplugRescansAfterDeviceChange(t *testing.T) {
	c := newTestController(t)
	backend := &hotplugBackend{}
	c.backend = backend
	onGUI(c.watchDevices)
	t.Cleanup(func() { onGUI(c.stopWatchingDevices) })
	waitForLonger(t, func() bool { return len(c.devices) == 1 && c.devices[0].Path == "/dev/new" })
}

func TestHotplugRescanWaitsForOperationToEnd(t *testing.T) {
	c := newTestController(t)
	backend := &hotplugBackend{}
	c.backend = backend
	c.hotplug = &debouncer{delay: 5 * time.Millisecond, fn: func() { onGUIAsync(c.hotplugRescan) }}
	t.Cleanup(func() { onGUI(c.hotplug.stop) })
	onGUI(func() {
		c.operation = operationFormatting
		c.hotplugRescan()
	})
	time.Sleep(30 * time.Millisecond)
	onGUI(func() {})
	if got := backend.scans.Load(); got != 0 {
		t.Fatalf("scans during an operation = %d, want 0", got)
	}
	onGUI(func() { c.operation = operationNone })
	waitFor(t, func() bool { return len(c.devices) == 1 })
}

// waitForLonger is waitFor for conditions that wait out hotplugSettle.
func waitForLonger(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * hotplugSettle)
	for time.Now().Before(deadline) {
		met := false
		onGUI(func() { met = condition() })
		if met {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

// onGUIAsync queues f for the GUI goroutine, as fyne.Do does.
func onGUIAsync(f func()) { serialDriver{}.DoFromGoroutine(f, false) }
