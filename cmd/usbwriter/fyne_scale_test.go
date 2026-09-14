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

func TestApplyUIScalePublishesChosenRatio(t *testing.T) {
	t.Setenv(scaleEnvKey, "")
	applyUIScale(1.2)
	if got := os.Getenv(scaleEnvKey); got != "1.2000" {
		t.Errorf("%s = %q, want \"1.2000\"", scaleEnvKey, got)
	}
}

func TestBaseWindowSizeKeepsPixelFootprint(t *testing.T) {
	for _, scale := range uiScaleChoices {
		size := baseWindowSize(scale)
		width := float64(size.Width) * scale
		height := float64(size.Height) * scale
		if width < baseWindowWidth-1 || width > baseWindowWidth+1 {
			t.Errorf("baseWindowSize(%v) width footprint %v, want ~%v", scale, width, baseWindowWidth)
		}
		if height < baseWindowHeight-1 || height > baseWindowHeight+1 {
			t.Errorf("baseWindowSize(%v) height footprint %v, want ~%v", scale, height, baseWindowHeight)
		}
	}
	if got := baseWindowSize(0); got != baseWindowSize(defaultUIScale) {
		t.Errorf("baseWindowSize(0) = %v, want the default ratio size", got)
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
