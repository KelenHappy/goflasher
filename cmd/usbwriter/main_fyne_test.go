//go:build fyne && (linux || windows || darwin)

package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	core "github.com/goflasher/goflasher/internal/app"
	"github.com/goflasher/goflasher/internal/i18n"
	"github.com/goflasher/goflasher/internal/progress"
)

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
	for _, state := range []core.State{core.Writing, core.Flushing, core.Verifying, core.Ejecting, core.Unmounting} {
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
