//go:build fyne && (linux || windows || darwin)

package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	core "github.com/goflasher/goflasher/internal/app"
	"github.com/goflasher/goflasher/internal/i18n"
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
