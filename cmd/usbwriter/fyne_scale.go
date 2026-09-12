//go:build fyne

package main

import (
	"os"
	"strconv"

	"fyne.io/fyne/v2"
)

const (
	scalePreference = "ui.scale"
	// Fyne reads both variables every time a canvas recalculates its scale, so
	// updating them and re-broadcasting the settings resizes a running window.
	scaleEnvKey            = "FYNE_SCALE"
	disableDPIDetectionKey = "FYNE_DISABLE_DPI_DETECTION"
	defaultUIScale         = 1.0
	// Fyne rounds the product of the system and user scales to one decimal, so
	// the compensated user scale never needs more precision than that.
	scaleDecimals = 4
)

// uiScaleChoices are the ratios offered in settings. The rendered size is the
// ratio alone: desktop scaling is cancelled out rather than multiplied in.
// Fyne rounds the scale it renders at to one decimal, so every ratio sits on
// that grid and the percentage shown is the percentage applied.
var uiScaleChoices = []float64{0.8, 1.0, 1.2, 1.4, 1.6, 1.8, 2.0}

func loadUIScale(preferences fyne.Preferences) float64 {
	return knownUIScale(preferences.Float(scalePreference))
}

// knownUIScale rejects anything outside the offered ratios so a hand-edited
// preferences file cannot render the window unusable.
func knownUIScale(value float64) float64 {
	for _, choice := range uiScaleChoices {
		if choice == value {
			return choice
		}
	}
	return defaultUIScale
}

// applyUIScale pins rendering to the chosen ratio. Fyne multiplies the user
// scale by whatever the desktop reports, so the ratio is divided by that factor
// first and DPI detection is switched off where Fyne would infer one instead.
func applyUIScale(scale float64) {
	os.Setenv(disableDPIDetectionKey, "true")
	os.Setenv(scaleEnvKey, formatUserScale(compensatedUserScale(scale, systemScaleFactor())))
}

func compensatedUserScale(scale, system float64) float64 {
	if system <= 0 {
		return scale
	}
	return scale / system
}

func formatUserScale(scale float64) string {
	return strconv.FormatFloat(scale, 'f', scaleDecimals, 64)
}

func uiScaleLabel(scale float64) string {
	return strconv.FormatFloat(scale*100, 'f', -1, 64) + "%"
}
