//go:build fyne && (linux || windows || darwin)

package main

import (
	"testing"

	core "github.com/goflasher/goflasher/internal/app"
	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/progress"
)

// installerRunUpdates is the update sequence of one Windows installer build.
var installerRunUpdates = []progress.Update{
	{Stage: progress.StageInspecting},
	{Stage: progress.StagePlanning},
	{Stage: progress.StageStagingWIM},
	{Stage: progress.StageStagingWIM, BytesProcessed: 2, TotalBytes: 4},
	{Stage: progress.StageStagingWIM, BytesProcessed: 4, TotalBytes: 4},
	{Stage: progress.StageSplittingWIM},
	{Stage: progress.StageSplittingWIM, BytesProcessed: 2, TotalBytes: 4},
	{Stage: progress.StageSplittingWIM, BytesProcessed: 4, TotalBytes: 4},
	{Stage: progress.StagePartitioning},
	{Stage: progress.StageFormatting},
	{Stage: progress.StageExtracting},
	{Stage: progress.StageExtracting, BytesProcessed: 1, TotalBytes: 4},
	{Stage: progress.StageExtracting, BytesProcessed: 4, TotalBytes: 4},
	{Stage: progress.StageFlushing},
	{Stage: progress.StageVerifyingFilesystem},
	{Stage: progress.StageVerifyingFilesystem, BytesProcessed: 2, TotalBytes: 4},
	{Stage: progress.StageVerifyingFilesystem, BytesProcessed: 4, TotalBytes: 4},
	{Stage: progress.StageEjecting},
}

func TestInstallerProgressIsMonotonic(t *testing.T) {
	previous := 0.0
	for _, u := range installerRunUpdates {
		got := installerProgress(u)
		if got < previous || got > 1 {
			t.Fatalf("installerProgress(%+v) = %v after %v", u, got, previous)
		}
		previous = got
	}
	for _, stage := range []progress.Stage{progress.StageStagingWIM, progress.StageSplittingWIM, progress.StageExtracting} {
		start := installerProgress(progress.Update{Stage: stage})
		if mid := installerProgress(progress.Update{Stage: stage, BytesProcessed: 1, TotalBytes: 2}); mid <= start {
			t.Fatalf("%s at half = %v, want visible progress past %v", stage, mid, start)
		}
	}
}

func TestInstallerRunIgnoresVerifyOption(t *testing.T) {
	c := newTestController(t)
	c.installerRun = true
	extracting := progress.Update{Stage: progress.StageExtracting, BytesProcessed: 1, TotalBytes: 2}
	c.view.verifyCheck.SetChecked(true)
	checked := c.runProgress(extracting)
	c.view.verifyCheck.SetChecked(false)
	if unchecked := c.runProgress(extracting); checked != unchecked || checked == 0 {
		t.Fatalf("progress checked=%v unchecked=%v, want equal and non-zero", checked, unchecked)
	}
}

func TestProgressMetricsOnlyClaimFinalizingWhenFinalizing(t *testing.T) {
	c := newTestController(t)
	for stage, want := range map[progress.Stage]string{
		progress.StageInspecting:   "metrics.empty",
		progress.StagePartitioning: "metrics.empty",
		progress.StageFlushing:     "metrics.finalizing",
		progress.StageEjecting:     "metrics.finalizing",
	} {
		if got := c.progressMetrics(progress.Update{Stage: stage}); got != c.tr.T(want) {
			t.Errorf("%s metrics = %q, want %q", stage, got, c.tr.T(want))
		}
	}
}

func selectionFixture(t *testing.T, states ...core.State) (*guiController, device.Device) {
	t.Helper()
	c := newTestController(t)
	c.machine = stateMachineAt(t, states...)
	d := device.Device{ID: "usb-1", Path: "/dev/sdz", Vendor: "V", Model: "M", Serial: "S1", Size: 8 << 30}
	c.devices = []device.Device{d}
	c.view.deviceSelect.Options = []string{deviceDisplay(d)}
	c.view.deviceSelect.SetSelected(deviceDisplay(d))
	c.selected = d
	c.info.Path = "/images/test.img"
	return c, d
}

func TestRescanForgetsRemovedDevice(t *testing.T) {
	for _, states := range [][]core.State{
		{core.DeviceSelected, core.Ready},
		{core.DeviceSelected, core.Ready, core.Confirming, core.Failed},
	} {
		c, _ := selectionFixture(t, states...)
		c.showDevices(nil, device.ScanReport{})
		if c.selected.Path != "" || c.view.deviceSelect.Selected != "" {
			t.Fatalf("selection survived removal: %+v / %q", c.selected, c.view.deviceSelect.Selected)
		}
		if got := c.machine.State(); got != core.ImageSelected {
			t.Fatalf("state = %s, want ImageSelected", got)
		}
		if !c.view.format.Disabled() || c.view.start.Text != c.tr.T("action.start") {
			t.Fatalf("format disabled=%v start=%q", c.view.format.Disabled(), c.view.start.Text)
		}
	}
}

func TestRescanRefreshesPresentDevice(t *testing.T) {
	c, d := selectionFixture(t, core.DeviceSelected, core.Ready)
	fresh := d
	fresh.Mounted = true
	fresh.PartitionCount = 2
	c.showDevices([]device.Device{fresh}, device.ScanReport{})
	if !c.selected.Mounted || c.selected.PartitionCount != 2 {
		t.Fatalf("selected = %+v, want fresh snapshot", c.selected)
	}
	if got := c.machine.State(); got != core.Ready {
		t.Fatalf("state = %s, want Ready", got)
	}
}

func TestRescanDuringOperationKeepsSelection(t *testing.T) {
	c, d := selectionFixture(t, core.DeviceSelected, core.Ready, core.Confirming)
	c.showDevices(nil, device.ScanReport{})
	if c.selected.ID != d.ID || c.view.deviceSelect.Selected != deviceDisplay(d) {
		t.Fatalf("selection changed during an operation: %+v", c.selected)
	}
}
