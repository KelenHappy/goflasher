//go:build fyne

package main

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

func TestReadableThemeUsesWhiteTextInDarkMode(t *testing.T) {
	appTheme := newReadableTheme(themeModeDark)
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
	appTheme := newReadableTheme(themeModeLight)
	got := appTheme.Color(theme.ColorNameDisabled, theme.VariantLight)
	want := theme.DefaultTheme().Color(theme.ColorNameDisabled, theme.VariantLight)
	if color.NRGBAModel.Convert(got) != color.NRGBAModel.Convert(want) {
		t.Errorf("light disabled = %v, want %v", got, want)
	}
}

func TestThemeModeForcesRequestedPalette(t *testing.T) {
	dark := newReadableTheme(themeModeDark).Color(theme.ColorNameBackground, theme.VariantLight)
	wantDark := theme.DarkTheme().Color(theme.ColorNameBackground, theme.VariantLight)
	if color.NRGBAModel.Convert(dark) != color.NRGBAModel.Convert(wantDark) {
		t.Errorf("dark background = %v, want %v", dark, wantDark)
	}
	light := newReadableTheme(themeModeLight).Color(theme.ColorNameBackground, theme.VariantDark)
	wantLight := theme.LightTheme().Color(theme.ColorNameBackground, theme.VariantDark)
	if color.NRGBAModel.Convert(light) != color.NRGBAModel.Convert(wantLight) {
		t.Errorf("light background = %v, want %v", light, wantLight)
	}
	foreground := newReadableTheme(themeModeLight).Color(theme.ColorNameForeground, theme.VariantDark)
	wantForeground := theme.LightTheme().Color(theme.ColorNameForeground, theme.VariantLight)
	if color.NRGBAModel.Convert(foreground) != color.NRGBAModel.Convert(wantForeground) {
		t.Errorf("light foreground under dark OS variant = %v, want %v", foreground, wantForeground)
	}
}
