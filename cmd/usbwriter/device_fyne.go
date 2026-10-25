//go:build fyne && (linux || windows || darwin)

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"fyne.io/fyne/v2"

	core "github.com/goflasher/goflasher/internal/app"
	"github.com/goflasher/goflasher/internal/device"
)

func deviceDisplay(d device.Device) string {
	return fmt.Sprintf("%s %s · %.1f GB · %s", d.Vendor, d.Model, float64(d.Size)/1e9, d.Path)
}

func (c *guiController) refresh() {
	// A generation makes the most recently requested scan authoritative; device
	// enumeration can otherwise complete out of order.
	c.refreshGeneration++
	generation := c.refreshGeneration
	if c.refreshCancel != nil {
		c.refreshCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.refreshCancel = cancel
	go func() {
		var list []device.Device
		var report device.ScanReport
		err := c.runRecovered("device scan", func() (err error) {
			list, report, err = c.backend.ListAllowedDevices(ctx)
			return err
		})
		fyne.Do(func() { c.finishRefresh(generation, list, report, err) })
	}()
}

// finishRefresh runs on the Fyne thread and drops results from a superseded scan.
func (c *guiController) finishRefresh(generation uint64, list []device.Device, report device.ScanReport, err error) {
	if generation != c.refreshGeneration {
		return
	}
	c.refreshCancel = nil
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			c.view.status.SetText(c.tr.T("error.devices", err))
			c.logEvent(slog.LevelError, "error.devices", err)
		}
		return
	}
	c.showDevices(list, report)
}

func (c *guiController) showDevices(list []device.Device, report device.ScanReport) {
	c.devices = list
	options := make([]string, len(list))
	for i, d := range list {
		options[i] = deviceDisplay(d)
	}
	c.view.deviceSelect.Options = options
	c.view.deviceSelect.Refresh()
	c.reconcileSelection()
	c.logEvent(slog.LevelInfo, "log.devices", len(list))
	for _, d := range list {
		c.logger.Debug("device available", deviceAttrs(d)...)
	}
	// A device that failed inspection never reaches the list, so without this
	// line an unreadable device and an absent one look identical to the user.
	if report.Skipped > 0 {
		c.logEvent(slog.LevelWarn, "log.devices.skipped", report.Skipped)
	}
}

func (c *guiController) selectDevice(value string) {
	for _, d := range c.devices {
		if deviceDisplay(d) != value {
			continue
		}
		c.selected = d
		c.updateDeviceDetail()
		advanceSelection(c.machine, c.info.Path != "", core.ImageSelected, core.DeviceSelected)
		c.view.format.Enable()
		return
	}
}

// reconcileSelection matches the selected device against a new scan. A device
// that is still present is replaced by its fresh snapshot; one that is gone is
// deselected, so the confirmation dialog never describes a removed device. An
// active operation keeps its own copy of the target and is left alone.
func (c *guiController) reconcileSelection() {
	if c.selected.Path == "" || c.busy() {
		return
	}
	for _, d := range c.devices {
		if device.SameIdentity(c.selected, d) && d.Size == c.selected.Size {
			c.selected = d
			c.view.deviceSelect.SetSelected(deviceDisplay(d))
			c.updateDeviceDetail()
			return
		}
	}
	c.forgetDevice()
}

func (c *guiController) forgetDevice() {
	c.selected = device.Device{}
	c.view.deviceSelect.ClearSelected()
	c.view.deviceDetail.SetText(c.tr.T("device.none"))
	c.view.format.Disable()
	retreatSelection(c.machine, c.info.Path != "")
	c.view.start.SetText(c.tr.T(startActionKey(c.machine.State())))
}

// retreatSelection returns an idle or finished machine to the state for "no
// device selected", keeping the image selection when there is one.
func retreatSelection(machine *core.StateMachine, imageSelected bool) {
	switch machine.State() {
	case core.Ready, core.DeviceSelected, core.Completed, core.Cancelled, core.Failed:
		_ = machine.Transition(core.Idle)
	default:
		return
	}
	if imageSelected {
		_ = machine.Transition(core.ImageSelected)
	}
}

// updateDeviceDetail deliberately avoids selectDevice: changing presentation
// must not alter the state machine or unlock formatting during an operation.
func (c *guiController) updateDeviceDetail() {
	d := c.selected
	c.view.deviceDetail.SetText(c.tr.T("device.details", d.Vendor, d.Model, float64(d.Size)/1e9, d.Path, d.Serial, localBool(c.tr, d.IsCardReader), localBool(c.tr, d.Mounted), d.PartitionCount))
}
