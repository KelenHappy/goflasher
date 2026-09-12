//go:build fyne

package main

import (
	"os"
	"testing"
)

func TestKnownUIScaleRejectsUnofferedRatios(t *testing.T) {
	for _, offered := range uiScaleChoices {
		if got := knownUIScale(offered); got != offered {
			t.Errorf("knownUIScale(%v) = %v, want %v", offered, got, offered)
		}
	}
	for _, rejected := range []float64{0, -1, 0.1, 1.3, 12} {
		if got := knownUIScale(rejected); got != defaultUIScale {
			t.Errorf("knownUIScale(%v) = %v, want %v", rejected, got, defaultUIScale)
		}
	}
}

func TestCompensatedUserScaleCancelsDesktopScaling(t *testing.T) {
	// Fyne renders at round(system*user, 1 decimal), so the compensated value
	// has to reproduce the requested ratio exactly at every desktop setting.
	for _, system := range []float64{1.0, 1.25, 1.5, 1.75, 2.0, 2.25, 3.0} {
		for _, requested := range uiScaleChoices {
			user := compensatedUserScale(requested, system)
			if rendered := roundToTenth(system * user); rendered != requested {
				t.Errorf("system %v with ratio %v rendered %v, want %v", system, requested, rendered, requested)
			}
		}
	}
}

func TestCompensatedUserScaleIgnoresUnusableSystemScale(t *testing.T) {
	if got := compensatedUserScale(1.25, 0); got != 1.25 {
		t.Errorf("compensatedUserScale with zero system scale = %v, want 1.25", got)
	}
}

func TestApplyUIScaleDisablesDPIDetection(t *testing.T) {
	t.Setenv(scaleEnvKey, "")
	t.Setenv(disableDPIDetectionKey, "")
	applyUIScale(1.5)
	if got := os.Getenv(disableDPIDetectionKey); got != "true" {
		t.Errorf("%s = %q, want \"true\"", disableDPIDetectionKey, got)
	}
	if os.Getenv(scaleEnvKey) == "" {
		t.Errorf("%s was not set", scaleEnvKey)
	}
}

func TestUIScaleLabel(t *testing.T) {
	labels := map[float64]string{0.8: "80%", 1.0: "100%", 1.2: "120%", 2.0: "200%"}
	for scale, want := range labels {
		if got := uiScaleLabel(scale); got != want {
			t.Errorf("uiScaleLabel(%v) = %q, want %q", scale, got, want)
		}
	}
}

func roundToTenth(value float64) float64 {
	return float64(int(value*10+0.5)) / 10
}
