//go:build fyne

package main

import (
	"math"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

const (
	scalePreference = "ui.scale"
	defaultUIScale  = 1.0
	// The window is measured in em of the unscaled body text so its pixel
	// footprint stays put when the ratio changes; only the content zooms.
	windowWidthEm  = 52
	windowHeightEm = 44
)

// uiScaleChoices are the ratios offered in settings. The ratio is applied by
// the theme on top of the desktop's own DPI factor, so 100% matches other
// applications on the same screen and rendering stays at native resolution.
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

// scaledSize applies the ratio to one theme size. Text sizes are rounded up to
// a whole pixel so glyphs never land on a fractional size such as 16.8.
func scaledSize(base float32, scale float64, text bool) float32 {
	size := float64(base) * scale
	if text {
		size = math.Ceil(size)
	}
	return float32(size)
}

// em measures fixed dimensions in multiples of the current body text size, so
// they follow the theme and the chosen ratio exactly as the text does.
func em(n float32) float32 {
	return n * theme.TextSize()
}

// baseWindowSize uses the unscaled text size: picking 200% zooms the content
// instead of doubling the window past the screen edge.
func baseWindowSize() fyne.Size {
	unit := theme.DefaultTheme().Size(theme.SizeNameText)
	return fyne.NewSize(windowWidthEm*unit, windowHeightEm*unit)
}

func uiScaleLabel(scale float64) string {
	return strconv.FormatFloat(scale*100, 'f', -1, 64) + "%"
}
