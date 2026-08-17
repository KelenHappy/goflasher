//go:build fyne

package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

type readableTheme struct {
	fyne.Theme
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

func newReadableTheme(mode themeMode) fyne.Theme {
	base := theme.DefaultTheme()
	switch mode {
	case themeModeLight:
		base = theme.LightTheme()
	case themeModeDark:
		base = theme.DarkTheme()
	}
	return readableTheme{Theme: base}
}

func (t readableTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if variant == theme.VariantDark {
		switch name {
		case theme.ColorNameForeground, theme.ColorNameDisabled, theme.ColorNamePlaceHolder:
			return color.White
		}
	}
	return t.Theme.Color(name, variant)
}
