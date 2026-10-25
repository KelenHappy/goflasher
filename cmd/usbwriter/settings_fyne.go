//go:build fyne && (linux || windows || darwin)

package main

import (
	"net/url"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/goflasher/goflasher/internal/i18n"
	"github.com/goflasher/goflasher/internal/legal"
)

// sourceCodeURL is shown in settings so users can obtain the corresponding
// source code as required by the GPLv3.
const sourceCodeURL = "https://github.com/KelenHappy/goflasher"

// sourceCodeLink falls back to plain text if the URL cannot be parsed.
func sourceCodeLink() fyne.CanvasObject {
	u, err := url.Parse(sourceCodeURL)
	if err != nil {
		return widget.NewLabel(sourceCodeURL)
	}
	return widget.NewHyperlink(sourceCodeURL, u)
}

func (c *guiController) showSettings() {
	languages := []settingChoice[i18n.Locale]{{"English", i18n.English}, {"繁體中文", i18n.TraditionalChinese}, {"简体中文", i18n.SimplifiedChinese}, {"日本語", i18n.Japanese}}
	language := newSettingSelect(languages, c.tr.Locale(), func(locale i18n.Locale) {
		c.app.Preferences().SetString(languagePreference, string(locale))
		c.setLanguage(locale)
	})
	themes := []settingChoice[themeMode]{{c.tr.T("settings.theme.system"), themeModeSystem}, {c.tr.T("settings.theme.light"), themeModeLight}, {c.tr.T("settings.theme.dark"), themeModeDark}}
	themeSelect := newSettingSelect(themes, loadThemeMode(c.app.Preferences()), c.setTheme)
	scales := make([]settingChoice[float64], len(uiScaleChoices))
	for i, choice := range uiScaleChoices {
		scales[i] = settingChoice[float64]{uiScaleLabel(choice), choice}
	}
	scaleSelect := newSettingSelect(scales, loadUIScale(c.app.Preferences()), c.setUIScale)
	content := container.NewGridWithColumns(2,
		widget.NewLabel(c.tr.T("settings.language")), language,
		widget.NewLabel(c.tr.T("settings.theme")), themeSelect,
		widget.NewLabel(c.tr.T("settings.scale")), scaleSelect,
		widget.NewLabel(c.tr.T("settings.source")), sourceCodeLink(),
		widget.NewLabel(c.tr.T("settings.licenses")), widget.NewButton(c.tr.T("action.licenses"), c.showLicenses),
	)
	dialog.NewCustom(c.tr.T("settings.title"), c.tr.T("settings.close"), content, c.view.window).Show()
}

// showLicenses presents the embedded notices as a collapsed accordion. GPLv3
// and LGPL require these texts to travel with the binary, so they are embedded
// rather than linked; the accordion keeps the dialog usable with one entry per
// component instead of one continuous wall of text.
func (c *guiController) showLicenses() {
	documents := legal.Documents()
	items := make([]*widget.AccordionItem, 0, len(documents))
	for _, document := range documents {
		items = append(items, widget.NewAccordionItem(document.Title, licenseDetail(document.Body)))
	}
	accordion := widget.NewAccordion(items...)
	// MultiOpen stays off so an opened entry collapses the previous one and the
	// dialog never grows past the window.
	panel := dialog.NewCustom(c.tr.T("licenses.title"), c.tr.T("settings.close"),
		container.NewVScroll(accordion), c.view.window)
	panel.Resize(licenseDialogSize(c.view.window.Canvas().Size()))
	panel.Show()
}

// licenseDetail renders one license body at a fixed height so that opening a
// long text scrolls within the entry rather than resizing the dialog.
func licenseDetail(body string) fyne.CanvasObject {
	text := widget.NewLabel(body)
	text.Wrapping = fyne.TextWrapWord
	text.TextStyle = fyne.TextStyle{Monospace: true}
	detail := container.NewVScroll(text)
	detail.SetMinSize(fyne.NewSize(0, em(licenseDetailHeightEm)))
	return detail
}

const (
	// licenseDetailHeightEm is the visible height of an opened license text.
	licenseDetailHeightEm = 23
	// licenseDialogMinWidthEm/HeightEm are the floor for windows too small to
	// divide; licenseDialogCoverage leaves the parent visible around the dialog.
	licenseDialogMinWidthEm  = 34
	licenseDialogMinHeightEm = 28
	licenseDialogCoverage    = 0.9
)

// licenseDialogSize fits the dialog to the window while leaving the parent
// visible, and keeps a floor for windows too small to divide.
func licenseDialogSize(canvas fyne.Size) fyne.Size {
	width := max(canvas.Width*licenseDialogCoverage, em(licenseDialogMinWidthEm))
	height := max(canvas.Height*licenseDialogCoverage, em(licenseDialogMinHeightEm))
	return fyne.NewSize(width, height)
}

// settingChoice and newSettingSelect keep presentation labels separate from
// persisted typed values. New settings can reuse this without JSON parsing or
// allowing unvalidated configuration to reach privileged operations.
type settingChoice[T comparable] struct {
	label string
	value T
}

func newSettingSelect[T comparable](choices []settingChoice[T], current T, changed func(T)) *widget.Select {
	labels := make([]string, len(choices))
	values := make(map[string]T, len(choices))
	selected := ""
	for i, choice := range choices {
		labels[i] = choice.label
		values[choice.label] = choice.value
		if choice.value == current {
			selected = choice.label
		}
	}
	control := widget.NewSelect(labels, func(label string) { changed(values[label]) })
	control.SetSelected(selected)
	return control
}

func (c *guiController) setTheme(mode themeMode) {
	c.app.Preferences().SetString(themePreference, string(mode))
	c.applyTheme()
}

// setUIScale only rebuilds the theme: the ratio lives in its sizes, so every
// open canvas re-lays out immediately while the window keeps its pixel size.
func (c *guiController) setUIScale(scale float64) {
	c.app.Preferences().SetFloat(scalePreference, knownUIScale(scale))
	c.applyTheme()
}

func (c *guiController) applyTheme() {
	preferences := c.app.Preferences()
	c.app.Settings().SetTheme(newReadableTheme(loadThemeMode(preferences), loadUIScale(preferences)))
}

func (c *guiController) setLanguage(locale i18n.Locale) {
	c.tr = i18n.New(string(locale))
	configureFyneTranslations(string(locale))
	c.translateControls()
	c.translateSelectionDetails()
	c.translateOperationStatus()
	c.view.window.Content().Refresh()
}

func (c *guiController) translateControls() {
	tr := c.tr
	c.view.window.SetTitle(tr.T("window.title"))
	c.view.verifyCheck.SetText(tr.T("option.verify"))
	c.view.ejectCheck.SetText(tr.T("option.eject"))
	c.view.start.SetText(tr.T(startActionKey(c.machine.State())))
	c.view.format.SetText(tr.T("action.format_fat32"))
	c.view.choose.SetText(tr.T("action.choose"))
	c.view.refresh.SetText(tr.T("action.rescan"))
	c.view.saveLog.SetText(tr.T("action.save_log"))
	c.view.settings.SetText(tr.T("action.settings"))
	c.view.logPanel.Items[0].Title = tr.T("log.details")
	c.view.deviceCard.SetTitle(tr.T("card.device"))
	c.view.imageCard.SetTitle(tr.T("card.image"))
	c.view.imageInfoCard.SetTitle(tr.T("card.image_info"))
	c.view.optionsCard.SetTitle(tr.T("card.options"))
	c.view.progressCard.SetTitle(tr.T("card.progress"))
}

func (c *guiController) translateSelectionDetails() {
	tr := c.tr
	if c.selected.Path == "" {
		c.view.deviceDetail.SetText(tr.T("device.none"))
	} else {
		c.updateDeviceDetail()
	}
	if c.info.Path == "" {
		c.view.imageInfo.SetText(tr.T("image.empty"))
	} else {
		c.view.imageInfo.SetText(tr.T("image.details", c.info.Format, c.info.Compression, float64(c.info.CompressedSize)/(1<<20)))
	}
}

func (c *guiController) translateOperationStatus() {
	if c.operation == operationFormatting {
		c.translateFormatStatus()
		return
	}
	if isReadyState(c.machine.State()) {
		c.view.status.SetText(c.tr.T("status.ready"))
		c.view.metrics.SetText(c.tr.T("metrics.empty"))
	}
}

func (c *guiController) translateFormatStatus() {
	status := "status.formatting"
	metrics := c.tr.T("metrics.empty")
	if c.formatProgress.Stage != "" {
		status = "stage." + string(c.formatProgress.Stage)
		metrics = c.tr.T("metrics.formatting", int(c.formatProgress.BytesProcessed))
	}
	c.view.status.SetText(c.tr.T(status))
	c.view.metrics.SetText(metrics)
}
