//go:build fyne && (linux || windows || darwin)

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	core "github.com/goflasher/goflasher/internal/app"
	"github.com/goflasher/goflasher/internal/progress"
)

func (c *guiController) startWrite() {
	if c.operation != operationNone {
		return
	}
	resetFinishedState(c.machine)
	if writingState(c.machine.State()) {
		c.cancelWrite()
		return
	}
	if c.machine.State() != core.Ready {
		dialog.ShowInformation(c.tr.T("dialog.not_ready.title"), c.tr.T("dialog.not_ready.body"), c.view.window)
		return
	}
	_ = c.machine.Transition(core.Confirming)
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.lock(true)
	c.view.start.SetText(c.tr.T("action.cancel"))
	c.view.status.SetText(c.tr.T("stage.inspecting"))
	c.view.bar.Show()
	c.view.bar.SetValue(0)
	go func() {
		var preview core.PlanSummary
		err := c.runRecovered("preview", func() (err error) {
			preview, err = (&core.Service{Backend: c.backend}).Preview(ctx, c.info, c.selected)
			return err
		})
		fyne.Do(func() { c.finishPreview(preview, err) })
	}()
}

func (c *guiController) finishPreview(preview core.PlanSummary, err error) {
	c.cancel = nil
	if c.closing {
		c.closeNow()
		return
	}
	if err != nil {
		c.finishPreviewError(err)
		return
	}
	c.installerRun = preview.Class == string(core.ImageWindowsISO)
	body := widget.NewLabel(c.previewConfirmationText(preview))
	body.Wrapping = fyne.TextWrapWord
	confirm := dialog.NewCustomConfirm(c.tr.T("confirm.title"), c.tr.T("confirm.accept"), c.tr.T("action.cancel"), body, c.confirmWrite, c.view.window)
	confirm.SetDismissText(c.tr.T("action.cancel"))
	confirm.Show()
}

func (c *guiController) finishPreviewError(err error) {
	_ = c.machine.Transition(core.Ready)
	c.lock(false)
	c.view.start.SetText(c.tr.T("action.start"))
	if errors.Is(err, context.Canceled) {
		c.view.status.SetText(c.tr.T("status.cancelled"))
		return
	}
	c.logEvent(slog.LevelError, "log.error", err)
	c.logger.Debug("preview failed", "kind", core.ErrorMessageID(err))
	dialog.ShowError(fmt.Errorf("%s: %w", c.tr.T(core.ErrorMessageID(err)), err), c.view.window)
}

func (c *guiController) previewConfirmationText(preview core.PlanSummary) string {
	bodyText := c.tr.T("confirm.body", c.selected.Vendor, c.selected.Model, c.selected.Path, float64(c.selected.Size)/1e9, c.selected.Serial, filepath.Base(c.info.Path), float64(c.info.CompressedSize)/(1<<20))
	if preview.Class == string(core.ImageWindowsISO) {
		split, reason := c.localizedSplitPlan(preview.SplitRequired)
		bodyText += "\n\n" + c.tr.T("plan.windows.summary", preview.PartitionTable, preview.Filesystem, preview.BootMode, split, reason, float64(preview.RequiredCapacity)/(1<<30), float64(preview.RequiredTemporarySpace)/(1<<30), float64(preview.AvailableTemporarySpace)/(1<<30))
	}
	return bodyText
}

func (c *guiController) localizedSplitPlan(required bool) (string, string) {
	if required {
		return c.tr.T("bool.true"), c.tr.T("plan.split.reason.fat32")
	}
	return c.tr.T("bool.false"), c.tr.T("plan.split.reason.none")
}

func writingState(state core.State) bool {
	switch state {
	case core.Confirming, core.Inspecting, core.Planning, core.StagingWIM,
		core.SplittingWIM, core.Partitioning, core.Formatting, core.Extracting,
		core.Writing, core.Flushing, core.Verifying, core.VerifyingFilesystem,
		core.Ejecting, core.Unmounting:
		return true
	default:
		return false
	}
}

func startActionKey(state core.State) string {
	switch {
	case writingState(state):
		return "action.cancel"
	case state == core.Completed:
		return "action.restart"
	case state == core.Cancelled || state == core.Failed:
		return "action.retry"
	default:
		return "action.start"
	}
}

func (c *guiController) cancelWrite() {
	if c.cancel != nil {
		c.cancel()
		c.view.status.SetText(c.tr.T("status.cancelling"))
		c.logEvent(slog.LevelWarn, "log.cancel")
	}
}

func (c *guiController) confirmWrite(ok bool) {
	if !ok {
		_ = c.machine.Transition(core.Ready)
		c.lock(false)
		c.view.start.SetText(c.tr.T("action.start"))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.view.start.SetText(c.tr.T("action.cancel"))
	c.view.status.SetText(c.tr.T("status.preparing"))
	c.view.bar.Show()
	c.view.bar.SetValue(0)
	c.logEvent(slog.LevelInfo, "log.start")
	c.logger.Debug("write requested", append([]any{"image", c.info.Path, "format", c.info.Format, "installer", c.installerRun, "verify", c.view.verifyCheck.Checked, "eject", c.view.ejectCheck.Checked}, deviceAttrs(c.selected)...)...)
	updates := make(chan progress.Update, 32)
	progressDone := make(chan struct{})
	go func() {
		c.consumeWriteProgress(updates)
		close(progressDone)
	}()
	go func() {
		var result core.RunResult
		err := c.runRecovered("write", func() (err error) {
			result, err = (&core.Service{Backend: c.backend, State: c.machine}).Run(ctx, core.RunRequest{Image: c.info, Target: c.selected, Options: core.RunOptions{Verify: c.view.verifyCheck.Checked, Eject: c.view.ejectCheck.Checked}, Updates: updates})
			return err
		})
		if errors.Is(err, errInternal) {
			// Run panicked before recording the outcome; a retry needs Failed.
			_ = c.machine.Transition(core.Failed)
		}
		close(updates)
		<-progressDone
		fyne.Do(func() { c.finishWrite(result, err) })
	}()
}

func (c *guiController) consumeWriteProgress(updates <-chan progress.Update) {
	var stage progress.Stage
	for update := range updates {
		u := update
		if u.Stage != stage {
			stage = u.Stage
			c.logger.Debug("stage", "stage", stage, "processed", u.BytesProcessed, "total", u.TotalBytes)
		}
		fyne.Do(func() {
			c.view.status.SetText(c.tr.T("stage." + string(u.Stage)))
			c.view.bar.SetValue(c.runProgress(u))
			c.view.metrics.SetText(c.progressMetrics(u))
		})
	}
}

func (c *guiController) finishWrite(result core.RunResult, runErr error) {
	c.cancel = nil
	if c.closing {
		c.closeNow()
		return
	}
	if runErr != nil {
		c.showWriteFailure(runErr)
	} else {
		c.showWriteSuccess(result)
	}
	c.lock(false)
}

func (c *guiController) showWriteFailure(runErr error) {
	c.view.status.SetText(c.tr.T("status.failed", userError(c.tr, runErr)))
	c.logEvent(slog.LevelError, "log.error", runErr)
	c.logger.Debug("write failed", "state", c.machine.State(), "kind", core.ErrorMessageID(runErr))
	if c.machine.State() == core.Cancelled {
		c.view.status.SetText(c.tr.T("status.cancelled"))
	}
	c.view.start.SetText(c.tr.T("action.retry"))
}

func (c *guiController) showWriteSuccess(result core.RunResult) {
	c.view.status.SetText(c.tr.T("status.complete"))
	c.view.bar.SetValue(1)
	if result.PlanKind == core.PlanWindowsInstaller {
		c.logEvent(slog.LevelInfo, "log.installer.complete", result.FilesWritten, result.WIMParts, localBool(c.tr, result.SemanticVerified), localBool(c.tr, result.Ejected), result.Elapsed.Round(time.Second))
	} else {
		c.logEvent(slog.LevelInfo, "log.complete", result.BytesWritten, localBool(c.tr, result.Verified), localBool(c.tr, result.Ejected), result.Elapsed.Round(time.Second))
	}
	c.logger.Debug("write result", "plan", result.PlanKind, "bytes", result.BytesWritten, "elapsed", result.Elapsed,
		"source_sha256", result.SourceSHA256, "target_sha256", result.TargetSHA256, "manifest_sha256", result.ManifestSHA256,
		"verified", result.Verified, "semantic_verification", result.SemanticVerification, "ejected", result.Ejected)
	c.view.metrics.SetText(c.tr.T("metrics.complete", result.AverageBytesPerSecond/(1<<20), result.Elapsed.Round(time.Second)))
	c.view.start.SetText(c.tr.T("action.restart"))
}

func (c *guiController) runProgress(update progress.Update) float64 {
	if c.installerRun {
		return installerProgress(update)
	}
	return overallProgress(update, c.view.verifyCheck.Checked)
}

func (c *guiController) progressMetrics(update progress.Update) string {
	switch {
	case update.TotalBytes > 0:
		return c.tr.T("metrics.progress", update.BytesPerSecond/(1<<20), float64(update.BytesProcessed)/(1<<20), update.ETA.Round(time.Second))
	case update.Stage == progress.StageFlushing, update.Stage == progress.StageEjecting:
		return c.tr.T("metrics.finalizing")
	default:
		return c.tr.T("metrics.empty")
	}
}
