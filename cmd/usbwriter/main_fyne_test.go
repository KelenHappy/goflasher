//go:build fyne && (linux || windows || darwin)

package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	core "github.com/goflasher/goflasher/internal/app"
	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/i18n"
	"github.com/goflasher/goflasher/internal/progress"
)

func TestReadyStateClassification(t *testing.T) {
	for _, state := range []core.State{core.Idle, core.ImageSelected, core.DeviceSelected, core.Ready} {
		if !isReadyState(state) {
			t.Errorf("state %v was not classified as ready", state)
		}
	}
	for _, state := range []core.State{core.Confirming, core.Writing, core.Verifying, core.Completed, core.Failed} {
		if isReadyState(state) {
			t.Errorf("active or terminal state %v was classified as ready", state)
		}
	}
}

type blockingFormatter struct {
	started chan struct{}
	release chan struct{}
}

func (f *blockingFormatter) FormatFAT32(_ context.Context, _ device.Device, _ string, updates chan<- progress.Update) error {
	close(f.started)
	updates <- progress.Update{Stage: progress.StageFormatting, BytesProcessed: 42, TotalBytes: 100}
	<-f.release
	return errors.New("test format stopped")
}

type cancellableFormatter struct {
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func (f *cancellableFormatter) FormatFAT32(ctx context.Context, _ device.Device, _ string, _ chan<- progress.Update) error {
	close(f.started)
	<-ctx.Done()
	close(f.cancelled)
	<-f.release
	return ctx.Err()
}

func TestCloseCancelsActiveFormatAndWaits(t *testing.T) {
	c := newTestController(t)
	c.selected = device.Device{Path: "/dev/test", Size: 1 << 30}
	formatter := &cancellableFormatter{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	c.runFormat(formatter, c.selected)
	<-formatter.started

	fyne.DoAndWait(c.closeWindow)
	<-formatter.cancelled
	if !c.closing || c.operation != operationFormatting {
		t.Fatal("window closed before formatting worker exited")
	}
	done := c.shutdownDone
	select {
	case <-done:
		t.Fatal("window closed before formatting worker exited")
	default:
	}
	close(formatter.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("window did not close after formatting worker exited")
	}
}

func TestCloseCancelsActiveWrite(t *testing.T) {
	c := newTestController(t)
	cancelled := make(chan struct{})
	c.cancel = func() { close(cancelled) }
	c.closeWindow()
	select {
	case <-cancelled:
	default:
		t.Fatal("active write was not cancelled")
	}
	if !c.closing {
		t.Fatal("window did not wait for active write")
	}
}

func TestFormattingExcludesWritingAndPreservesLocalizedStatus(t *testing.T) {
	c := newTestController(t)
	c.selected = device.Device{Path: "/dev/test", Size: 1 << 30}
	c.machine = stateMachineAt(t, core.ImageSelected, core.Ready)
	formatter := &blockingFormatter{started: make(chan struct{}), release: make(chan struct{})}

	c.runFormat(formatter, c.selected)
	<-formatter.started
	waitFor(t, func() bool { return c.formatProgress.BytesProcessed == 42 })
	if !c.view.start.Disabled() {
		t.Fatal("Start is enabled during formatting")
	}
	c.startWrite()
	if got := c.machine.State(); got != core.Ready {
		t.Fatalf("state after Start during format = %s, want Ready", got)
	}

	c.setLanguage(i18n.Japanese)
	if got, want := c.view.status.Text, "フォーマット中"; got != want {
		t.Errorf("status after language change = %q, want %q", got, want)
	}
	if got, want := c.view.metrics.Text, "進捗：42%"; got != want {
		t.Errorf("metrics after language change = %q, want %q", got, want)
	}
	if !c.view.start.Disabled() || !c.view.format.Disabled() {
		t.Error("destructive controls enabled by language change during formatting")
	}

	close(formatter.release)
	waitFor(t, func() bool { return c.operation == operationNone })
}

type scanResult struct {
	devices []device.Device
	started chan struct{}
	release chan struct{}
}

type orderedScanBackend struct {
	device.Backend
	mu      sync.Mutex
	results []scanResult
	next    int
}

func (b *orderedScanBackend) ListAllowedDevices(context.Context) ([]device.Device, error) {
	b.mu.Lock()
	r := b.results[b.next]
	b.next++
	b.mu.Unlock()
	close(r.started)
	<-r.release
	return r.devices, nil
}

func TestRefreshKeepsNewestResult(t *testing.T) {
	c := newTestController(t)
	first := make(chan struct{})
	second := make(chan struct{})
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	c.backend = &orderedScanBackend{results: []scanResult{
		{devices: []device.Device{{Path: "/dev/old"}}, started: firstStarted, release: first},
		{devices: []device.Device{{Path: "/dev/new"}}, started: secondStarted, release: second},
	}}
	c.refresh()
	<-firstStarted
	c.refresh()
	<-secondStarted
	close(second)
	waitFor(t, func() bool { return len(c.devices) == 1 && c.devices[0].Path == "/dev/new" })
	close(first)
	time.Sleep(20 * time.Millisecond)
	if got := c.devices[0].Path; got != "/dev/new" {
		t.Fatalf("device after stale refresh completed = %q, want /dev/new", got)
	}
}

func TestRefreshAndLanguageChangeShareGUIThread(t *testing.T) {
	c := newTestController(t)
	started := make(chan struct{})
	release := make(chan struct{})
	c.backend = &orderedScanBackend{results: []scanResult{{
		devices: []device.Device{{Path: "/dev/test"}}, started: started, release: release,
	}}}

	c.refresh()
	<-started
	for i := 0; i < 20; i++ {
		locale := i18n.Japanese
		if i%2 == 0 {
			locale = i18n.English
		}
		fyne.DoAndWait(func() { c.setLanguage(locale) })
	}
	close(release)
	waitFor(t, func() bool { return len(c.devices) == 1 })
}

func newTestController(t *testing.T) *guiController {
	t.Helper()
	a := fyneapp.NewWithID(fmt.Sprintf("org.goflasher.test.%d", time.Now().UnixNano()))
	t.Cleanup(a.Quit)
	w := a.NewWindow("test")
	tr := i18n.New("en")
	v := newApplicationView(tr, w)
	w.SetContent(windowContent(tr, v))
	return &guiController{tr: tr, view: v, machine: core.NewStateMachine(), app: a}
}

func TestThemeSelectionIsPersisted(t *testing.T) {
	c := newTestController(t)
	c.setTheme(themeModeDark)
	if got := loadThemeMode(c.app.Preferences()); got != themeModeDark {
		t.Fatalf("saved theme = %q, want %q", got, themeModeDark)
	}
}

func TestInvalidThemePreferenceFallsBackToSystem(t *testing.T) {
	c := newTestController(t)
	c.app.Preferences().SetString(themePreference, "untrusted-value")
	if got := loadThemeMode(c.app.Preferences()); got != themeModeSystem {
		t.Fatalf("loaded theme = %q, want %q", got, themeModeSystem)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	met := false
	for !met && time.Now().Before(deadline) {
		fyne.DoAndWait(func() { met = condition() })
		time.Sleep(time.Millisecond)
	}
	if !met {
		t.Fatal("timed out waiting for condition")
	}
}

func TestAdvanceSelection(t *testing.T) {
	tests := []struct {
		name                string
		initial             []core.State
		counterpartSelected bool
		waitingState        core.State
		selectedState       core.State
		want                core.State
	}{
		{name: "idle image selected", counterpartSelected: false, waitingState: core.DeviceSelected, selectedState: core.ImageSelected, want: core.ImageSelected},
		{name: "idle device selected", counterpartSelected: false, waitingState: core.ImageSelected, selectedState: core.DeviceSelected, want: core.DeviceSelected},
		{name: "device completes image selection", initial: []core.State{core.ImageSelected}, counterpartSelected: true, waitingState: core.ImageSelected, selectedState: core.DeviceSelected, want: core.Ready},
		{name: "image completes device selection", initial: []core.State{core.DeviceSelected}, counterpartSelected: true, waitingState: core.DeviceSelected, selectedState: core.ImageSelected, want: core.Ready},
		{name: "counterpart without waiting state", initial: []core.State{core.ImageSelected}, counterpartSelected: true, waitingState: core.DeviceSelected, selectedState: core.ImageSelected, want: core.ImageSelected},
		{name: "selection outside idle", initial: []core.State{core.DeviceSelected}, counterpartSelected: false, waitingState: core.ImageSelected, selectedState: core.DeviceSelected, want: core.DeviceSelected},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			machine := stateMachineAt(t, tt.initial...)
			advanceSelection(machine, tt.counterpartSelected, tt.waitingState, tt.selectedState)
			if got := machine.State(); got != tt.want {
				t.Fatalf("state = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestWritingState(t *testing.T) {
	for _, state := range []core.State{
		core.Confirming, core.Inspecting, core.Planning, core.StagingWIM,
		core.SplittingWIM, core.Partitioning, core.Formatting, core.Extracting,
		core.Writing, core.Flushing, core.Verifying, core.VerifyingFilesystem,
		core.Ejecting, core.Unmounting,
	} {
		if !writingState(state) {
			t.Errorf("writingState(%s) = false", state)
		}
	}
	for _, state := range []core.State{core.Idle, core.Ready, core.Completed, core.Cancelled, core.Failed} {
		if writingState(state) {
			t.Errorf("writingState(%s) = true", state)
		}
	}
}

func TestStartActionKey(t *testing.T) {
	tests := []struct {
		state core.State
		want  string
	}{
		{state: core.Idle, want: "action.start"},
		{state: core.Ready, want: "action.start"},
		{state: core.Completed, want: "action.restart"},
		{state: core.Cancelled, want: "action.retry"},
		{state: core.Failed, want: "action.retry"},
		{state: core.Unmounting, want: "action.cancel"},
		{state: core.Writing, want: "action.cancel"},
		{state: core.Flushing, want: "action.cancel"},
		{state: core.Verifying, want: "action.cancel"},
		{state: core.Ejecting, want: "action.cancel"},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := startActionKey(tt.state); got != tt.want {
				t.Fatalf("startActionKey(%s) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestProgressRatio(t *testing.T) {
	tests := []struct {
		name   string
		update progress.Update
		want   float64
	}{
		{name: "unknown total", update: progress.Update{BytesProcessed: 5}, want: 0},
		{name: "partial", update: progress.Update{BytesProcessed: 25, TotalBytes: 100}, want: 0.25},
		{name: "complete", update: progress.Update{BytesProcessed: 100, TotalBytes: 100}, want: 1},
		{name: "clamped", update: progress.Update{BytesProcessed: 150, TotalBytes: 100}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := progressRatio(tt.update); got != tt.want {
				t.Fatalf("progressRatio() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResetFinishedState(t *testing.T) {
	tests := []struct {
		name    string
		initial []core.State
		want    core.State
	}{
		{name: "completed", initial: []core.State{core.ImageSelected, core.Ready, core.Confirming, core.Unmounting, core.Writing, core.Flushing, core.Completed}, want: core.Ready},
		{name: "cancelled", initial: []core.State{core.ImageSelected, core.Ready, core.Confirming, core.Cancelled}, want: core.Ready},
		{name: "failed", initial: []core.State{core.ImageSelected, core.Ready, core.Confirming, core.Failed}, want: core.Ready},
		{name: "unfinished state unchanged", initial: []core.State{core.ImageSelected}, want: core.ImageSelected},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			machine := stateMachineAt(t, tt.initial...)
			resetFinishedState(machine)
			if got := machine.State(); got != tt.want {
				t.Fatalf("state = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestUserError(t *testing.T) {
	tr := i18n.New("zh-TW")
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "direct cancellation", err: context.Canceled, want: "操作已取消"},
		{name: "wrapped cancellation", err: fmt.Errorf("write interrupted: %w", context.Canceled), want: "操作已取消"},
		{name: "other error", err: errors.New("disk unavailable"), want: "disk unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := userError(tr, tt.err); got != tt.want {
				t.Fatalf("userError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func stateMachineAt(t *testing.T, transitions ...core.State) *core.StateMachine {
	t.Helper()
	machine := core.NewStateMachine()
	for _, next := range transitions {
		if err := machine.Transition(next); err != nil {
			t.Fatalf("prepare state %s -> %s: %v", machine.State(), next, err)
		}
	}
	return machine
}
