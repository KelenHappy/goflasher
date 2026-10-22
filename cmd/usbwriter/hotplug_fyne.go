//go:build fyne && (linux || windows || darwin)

package main

import (
	"context"
	"sync"
	"time"

	"fyne.io/fyne/v2"

	"github.com/goflasher/goflasher/internal/device"
)

// hotplugSettle coalesces the burst of events one attach or detach produces,
// a disk and then each of its partitions, into one rescan. While an operation
// runs it is also how often a deferred rescan checks whether it may go ahead.
const hotplugSettle = 750 * time.Millisecond

// watchDevices rescans after devices come or go, on backends that report it.
// Rescan stays available for the rest.
func (c *guiController) watchDevices() {
	watcher, ok := c.backend.(device.HotplugBackend)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.watchCancel = cancel
	c.hotplug = &debouncer{delay: hotplugSettle, fn: func() { fyne.Do(c.hotplugRescan) }}
	settle := c.hotplug
	go func() {
		err := watcher.WatchDevices(ctx, settle.trigger)
		settle.stop()
		if err != nil {
			fyne.Do(func() { c.appendLog(c.tr.T("log.hotplug.unavailable", err)) })
		}
	}()
}

// hotplugRescan runs on the Fyne thread. It does not rescan during an
// operation, which itself makes the target's partitions come and go, but
// checks again after hotplugSettle until the operation has ended.
func (c *guiController) hotplugRescan() {
	if c.busy() {
		c.hotplug.trigger()
		return
	}
	c.refresh()
}

func (c *guiController) stopWatchingDevices() {
	if c.watchCancel != nil {
		c.watchCancel()
		c.watchCancel = nil
	}
	if c.hotplug != nil {
		c.hotplug.stop()
	}
}

// debouncer runs fn on its own goroutine once delay has passed without
// another trigger. It is safe for concurrent use.
type debouncer struct {
	delay   time.Duration
	fn      func()
	mu      sync.Mutex
	timer   *time.Timer
	stopped bool
}

func (d *debouncer) trigger() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	if d.timer == nil {
		d.timer = time.AfterFunc(d.delay, d.fn)
		return
	}
	d.timer.Reset(d.delay)
}

func (d *debouncer) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
	}
}
