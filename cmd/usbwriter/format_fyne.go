//go:build fyne && (linux || windows || darwin)

package main

import (
	"context"
	"log/slog"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/progress"
)

func (c *guiController) formatDevice() {
	formatter, ok := c.backend.(device.FAT32Formatter)
	if !ok || c.selected.Path == "" {
		dialog.ShowInformation(c.tr.T("dialog.not_ready.title"), c.tr.T("dialog.format.not_ready"), c.view.window)
		return
	}
	body := widget.NewLabel(c.tr.T("format.confirm.body", c.selected.Vendor, c.selected.Model, c.selected.Path, float64(c.selected.Size)/1e9, c.selected.Serial))
	body.Wrapping = fyne.TextWrapWord
	confirm := dialog.NewCustomConfirm(c.tr.T("format.confirm.title"), c.tr.T("format.confirm.accept"), c.tr.T("action.cancel"), body, func(ok bool) {
		if ok {
			c.runFormat(formatter, c.selected)
		}
	}, c.view.window)
	confirm.SetDismissText(c.tr.T("action.cancel"))
	confirm.Show()
}

func (c *guiController) runFormat(formatter device.FAT32Formatter, target device.Device) {
	if c.busy() {
		return
	}
	c.operation = operationFormatting
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.formatProgress = progress.Update{}
	c.lock(true)
	c.view.start.Disable()
	c.view.status.SetText(c.tr.T("status.formatting"))
	c.view.bar.SetValue(0)
	c.view.bar.Show()
	c.view.metrics.SetText(c.tr.T("metrics.empty"))
	c.logEvent(slog.LevelInfo, "log.format.start", target.Path)
	c.logger.Debug("format requested", deviceAttrs(target)...)
	updates := make(chan progress.Update, 100)
	progressDone := make(chan struct{})
	go func() {
		c.consumeFormatProgress(updates)
		close(progressDone)
	}()
	go func() {
		err := c.runRecovered("format", func() error {
			return formatter.FormatFAT32(ctx, target, "GOFLASHER", updates)
		})
		close(updates)
		<-progressDone
		fyne.Do(func() { c.finishFormat(err) })
	}()
}

func (c *guiController) consumeFormatProgress(updates <-chan progress.Update) {
	for update := range updates {
		u := update
		fyne.Do(func() {
			c.formatProgress = u
			c.view.bar.SetValue(overallProgress(u, false))
			c.view.status.SetText(c.tr.T("stage." + string(u.Stage)))
			c.view.metrics.SetText(c.tr.T("metrics.formatting", int(u.BytesProcessed)))
		})
	}
}

func (c *guiController) finishFormat(err error) {
	c.cancel = nil
	c.operation = operationNone
	c.formatProgress = progress.Update{}
	c.lock(false)
	c.view.start.Enable()
	if c.closing {
		c.closeNow()
		return
	}
	c.view.bar.Show()
	if err != nil {
		c.view.status.SetText(c.tr.T("status.failed", err))
		c.logEvent(slog.LevelError, "log.error", err)
		dialog.ShowError(err, c.view.window)
		return
	}
	c.view.status.SetText(c.tr.T("status.format.complete"))
	c.view.bar.SetValue(1)
	c.logEvent(slog.LevelInfo, "log.format.complete")
	c.refresh()
}
