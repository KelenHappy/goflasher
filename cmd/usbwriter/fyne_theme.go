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

func newReadableTheme() fyne.Theme {
	return readableTheme{Theme: theme.DefaultTheme()}
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
