//go:build fyne && (linux || windows || darwin)

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/progress"
)

func TestRunRecoveredTurnsPanicIntoError(t *testing.T) {
	c := newTestController(t)
	err := c.runRecovered("test work", func() error { panic("boom") })
	if !errors.Is(err, errInternal) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("runRecovered() = %v, want an errInternal naming the panic", err)
	}
	history := strings.Join(c.history.Lines(), "\n")
	for _, want := range []string{"panic recovered", "operation=\"test work\"", "stack="} {
		if !strings.Contains(history, want) {
			t.Errorf("history lacks %q:\n%s", want, history)
		}
	}
}

func TestRunRecoveredPassesErrorsThrough(t *testing.T) {
	c := newTestController(t)
	want := errors.New("plain failure")
	if err := c.runRecovered("test work", func() error { return want }); err != want {
		t.Fatalf("runRecovered() = %v, want %v", err, want)
	}
}

type panickingFormatter struct{}

func (panickingFormatter) FormatFAT32(context.Context, device.Device, string, chan<- progress.Update) error {
	panic("formatter bug")
}

func TestFormatPanicIsReportedAndReleasesTheDevice(t *testing.T) {
	c := newTestController(t)
	c.selected = device.Device{Path: "/dev/test", Size: 1 << 30}
	onGUI(func() { c.runFormat(panickingFormatter{}, c.selected) })
	waitFor(t, func() bool { return c.operation == operationNone })
	if !strings.Contains(c.view.logs.Text, "formatter bug") {
		t.Fatalf("on-screen log does not report the panic:\n%s", c.view.logs.Text)
	}
}
