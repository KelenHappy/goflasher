//go:build fyne

package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

type readableTheme struct {
	fyne.Theme
	mode  themeMode
	scale float64
}

type themeMode string

const (
	themeModeSystem themeMode = "system"
	themeModeLight  themeMode = "light"
	themeModeDark   themeMode = "dark"
	themePreference           = "theme"
)

func loadThemeMode(preferences fyne.Preferences) themeMode {
	mode := themeMode(preferences.String(themePreference))
	if mode != themeModeLight && mode != themeModeDark {
		return themeModeSystem
	}
	return mode
}

func newReadableTheme(mode themeMode, scale float64) fyne.Theme {
	base := theme.DefaultTheme()
	switch mode {
	case themeModeLight:
		base = theme.LightTheme()
	case themeModeDark:
		base = theme.DarkTheme()
	}
	return readableTheme{Theme: base, mode: mode, scale: knownUIScale(scale)}
}

// Size scales every theme dimension by the chosen ratio. Widgets size
// themselves from these values, so one ratio zooms the whole interface.
func (t readableTheme) Size(name fyne.ThemeSizeName) float32 {
	return scaledSize(t.Theme.Size(name), t.scale, isTextSize(name))
}

func isTextSize(name fyne.ThemeSizeName) bool {
	switch name {
	case theme.SizeNameText, theme.SizeNameHeadingText, theme.SizeNameSubHeadingText, theme.SizeNameCaptionText:
		return true
	default:
		return false
	}
}

func (t readableTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	effectiveVariant := variant
	if t.mode == themeModeLight {
		effectiveVariant = theme.VariantLight
	} else if t.mode == themeModeDark {
		effectiveVariant = theme.VariantDark
	}
	if effectiveVariant == theme.VariantDark {
		switch name {
		case theme.ColorNameForeground, theme.ColorNameDisabled, theme.ColorNamePlaceHolder:
			return color.White
		}
	}
	return t.Theme.Color(name, effectiveVariant)
}
