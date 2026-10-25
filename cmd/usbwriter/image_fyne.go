//go:build fyne && (linux || windows || darwin)

package main

import (
	"log/slog"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	core "github.com/goflasher/goflasher/internal/app"
	"github.com/goflasher/goflasher/internal/image"
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
		err := c.runRecovered("image detection", func() (err error) {
			detected, err = image.Detect(path)
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
			c.view.imageInfo.SetText(c.tr.T("image.details", c.info.Format, c.info.Compression, float64(c.info.CompressedSize)/(1<<20)))
			advanceSelection(c.machine, c.selected.Path != "", core.DeviceSelected, core.ImageSelected)
			c.logEvent(slog.LevelInfo, "log.image", filepath.Base(path))
			c.logger.Debug("image detected", "image", path, "format", detected.Format, "compression", detected.Compression, "size", detected.CompressedSize, "uncompressed_size", detected.UncompressedSize)
		})
	}()
}
