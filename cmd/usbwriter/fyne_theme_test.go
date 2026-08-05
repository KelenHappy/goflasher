//go:build fyne

package main

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

func TestReadableThemeUsesWhiteTextInDarkMode(t *testing.T) {
	appTheme := newReadableTheme()
	for _, name := range []fyne.ThemeColorName{
		theme.ColorNameForeground,
		theme.ColorNameDisabled,
		theme.ColorNamePlaceHolder,
	} {
		got := appTheme.Color(name, theme.VariantDark)
		if color.NRGBAModel.Convert(got) != color.NRGBAModel.Convert(color.White) {
			t.Errorf("dark %s = %v, want white", name, got)
		}
	}
}

func TestReadableThemeKeepsLightModeContrast(t *testing.T) {
	appTheme := newReadableTheme()
	got := appTheme.Color(theme.ColorNameDisabled, theme.VariantLight)
	want := theme.DefaultTheme().Color(theme.ColorNameDisabled, theme.VariantLight)
	if color.NRGBAModel.Convert(got) != color.NRGBAModel.Convert(want) {
		t.Errorf("light disabled = %v, want %v", got, want)
	}
}
