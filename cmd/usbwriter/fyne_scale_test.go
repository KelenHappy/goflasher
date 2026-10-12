//go:build fyne

package main

import (
	"math"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
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

func TestThemeRoundsTextSizesUp(t *testing.T) {
	textSizes := []fyne.ThemeSizeName{theme.SizeNameText, theme.SizeNameHeadingText, theme.SizeNameSubHeadingText, theme.SizeNameCaptionText}
	for _, scale := range uiScaleChoices {
		scaled := newReadableTheme(themeModeSystem, scale)
		for _, name := range textSizes {
			base := float64(theme.DefaultTheme().Size(name))
			got := float64(scaled.Size(name))
			if got != math.Ceil(base*scale) {
				t.Errorf("scale %v %s = %v, want %v", scale, name, got, math.Ceil(base*scale))
			}
		}
	}
	// 14 x 1.2 = 16.8 is the case that motivated rounding up.
	if got := newReadableTheme(themeModeSystem, 1.2).Size(theme.SizeNameText); got != 17 {
		t.Errorf("120%% text size = %v, want 17", got)
	}
}

func TestThemeScalesNonTextSizesProportionally(t *testing.T) {
	scaled := newReadableTheme(themeModeSystem, 2.0)
	base := theme.DefaultTheme()
	for _, name := range []fyne.ThemeSizeName{theme.SizeNamePadding, theme.SizeNameInlineIcon, theme.SizeNameModalBlurRadius} {
		if got, want := scaled.Size(name), base.Size(name)*2; got != want {
			t.Errorf("200%% %s = %v, want %v", name, got, want)
		}
	}
}

func TestBaseWindowSizeIgnoresUIScale(t *testing.T) {
	unit := theme.DefaultTheme().Size(theme.SizeNameText)
	want := fyne.NewSize(windowWidthEm*unit, windowHeightEm*unit)
	if got := baseWindowSize(); got != want {
		t.Errorf("baseWindowSize() = %v, want %v", got, want)
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
