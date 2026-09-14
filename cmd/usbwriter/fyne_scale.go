//go:build fyne

package main

import (
	"os"
	"strconv"

	"fyne.io/fyne/v2"
)

const (
	scalePreference = "ui.scale"
	// Fyne reads FYNE_SCALE every time a canvas recalculates its scale, so
	// updating it and re-broadcasting the settings rescales a running window.
	scaleEnvKey    = "FYNE_SCALE"
	defaultUIScale = 1.0
	scaleDecimals  = 4
	// baseWindowWidth/Height describe the layout at 100%; other ratios keep the
	// same pixel footprint and zoom the content within it.
	baseWindowWidth  = 720
	baseWindowHeight = 620
)

// uiScaleChoices are the ratios offered in settings. Each ratio multiplies the
// scale the desktop already applies, so 100% matches other applications on the
// same screen and rendering stays at the display's native DPI. Cancelling the
// desktop factor out (the previous approach) forced a 1x framebuffer that
// compositors then bitmap-upscaled, which blurred the whole window.
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

// applyUIScale publishes the chosen ratio as Fyne's user scale, leaving the
// desktop's own DPI factor in place underneath it.
func applyUIScale(scale float64) {
	os.Setenv(scaleEnvKey, formatUserScale(scale))
}

// baseWindowSize converts the 100% layout size to the logical size that keeps
// the window's pixel footprint constant at the given ratio: picking 200% zooms
// the content instead of doubling the window past the screen edge.
func baseWindowSize(scale float64) fyne.Size {
	if scale <= 0 {
		scale = defaultUIScale
	}
	return fyne.NewSize(float32(baseWindowWidth/scale), float32(baseWindowHeight/scale))
}

func formatUserScale(scale float64) string {
	return strconv.FormatFloat(scale, 'f', scaleDecimals, 64)
}

func uiScaleLabel(scale float64) string {
	return strconv.FormatFloat(scale*100, 'f', -1, 64) + "%"
}
