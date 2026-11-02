//go:build fyne && (linux || windows || darwin)

package main

import (
	"log/slog"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	core "github.com/goflasher/goflasher/internal/app"
	"github.com/goflasher/goflasher/internal/i18n"
	"github.com/goflasher/goflasher/internal/image"
	"github.com/goflasher/goflasher/internal/osinfo"
)

// imagePickerLabels holds the localized text shown by the image chooser.
type imagePickerLabels struct {
	Title   string
	Accept  string
	Dismiss string
	Filter  string
}

func (c *guiController) chooseImage() {
	c.view.choose.Disable()
	labels := imagePickerLabels{
		Title:   c.tr.T("picker.image.title"),
		Accept:  c.tr.T("picker.image.accept"),
		Dismiss: c.tr.T("action.cancel"),
		Filter:  c.tr.T("filter.images"),
	}
	openImage(c.view.window, labels, func(path string, err error) {
		c.view.choose.Enable()
		if err != nil {
			dialog.ShowError(err, c.view.window)
			return
		}
		if path != "" {
			c.detectImage(path)
		}
	})
}

func (c *guiController) detectImage(path string) {
	// Registered before any record mentions path, including Detect's errors.
	c.history.RedactDir(filepath.Dir(path))
	go func() {
		var detected image.Info
		var system osinfo.System
		var systemErr error
		err := c.runRecovered("image detection", func() (err error) {
			detected, err = image.Detect(path)
			if err == nil && identifiable(detected) {
				system, systemErr = osinfo.IdentifyFile(path)
			}
			return err
		})
		if err != nil {
			fyne.Do(func() {
				c.logEvent(slog.LevelError, "log.error", err)
				dialog.ShowError(err, c.view.window)
			})
			return
		}
		fyne.Do(func() {
			c.info = detected
			c.view.imagePath.SetText(path)
			details := c.tr.T("image.details", c.info.Format, c.info.Compression, float64(c.info.CompressedSize)/(1<<20))
			if identifiable(detected) {
				details += "\n" + systemDetails(c.tr, system, systemErr)
			}
			c.view.imageInfo.SetText(details)
			advanceSelection(c.machine, c.selected.Path != "", core.DeviceSelected, core.ImageSelected)
			c.logEvent(slog.LevelInfo, "log.image", filepath.Base(path))
			c.logger.Debug("image detected", "image", path, "format", detected.Format, "compression", detected.Compression, "size", detected.CompressedSize, "uncompressed_size", detected.UncompressedSize)
			c.logger.Debug("operating system identified", "system", system.Summary(), "label", system.Label, "editions", len(system.Editions), "error", systemErr)
		})
	}()
}

// identifiable reports whether the image is an ISO osinfo can read in place.
// Compressed images would have to be decoded in full first, so they are left
// unidentified.
func identifiable(info image.Info) bool {
	return info.Format == image.FormatISO && info.Compression == image.CompressionNone
}

// systemDetails describes the identified operating system for the image
// panel. It is display text only and never steers how the image is written.
func systemDetails(tr i18n.Localizer, system osinfo.System, err error) string {
	if err != nil || !system.Known() {
		return tr.T("image.os.unknown")
	}
	text := tr.T("image.os", system.Summary())
	if len(system.Editions) != 0 {
		text += "\n" + tr.T("image.os.editions", strings.Join(system.Editions, ", "))
	}
	return text
}
