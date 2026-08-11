//go:build fyne && (linux || windows || darwin)

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	core "github.com/goflasher/goflasher/internal/app"
	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/i18n"
	"github.com/goflasher/goflasher/internal/image"
	"github.com/goflasher/goflasher/internal/progress"
)

func main() {
	if dispatchEmbeddedHelper() {
		return
	}
	runApplication(i18n.System())
}

func runApplication(tr i18n.Localizer) {
	controller := newGUIController(tr)
	controller.bindActions()
	controller.view.window.SetContent(container.NewVScroll(windowContent(tr, controller.view)))
	controller.appendLog(tr.T("log.launched"))
	controller.refresh()
	controller.view.window.ShowAndRun()
}

type applicationView struct {
	window                                   fyne.Window
	deviceSelect                             *widget.Select
	deviceDetail, imageInfo, status, metrics *widget.Label
	imagePath                                *widget.Entry
	verifyCheck, ejectCheck                  *widget.Check
	bar                                      *widget.ProgressBar
	logs                                     *widget.Entry
	logPanel                                 *widget.Accordion
	start, format, choose, refresh, copyLog  *widget.Button
	copyError                                *widget.Button
}

type guiController struct {
	tr       i18n.Localizer
	view     *applicationView
	backend  device.Backend
	machine  *core.StateMachine
	devices  []device.Device
	selected device.Device
	info     image.Info
	cancel   context.CancelFunc
	logLines []string
}

func newGUIController(tr i18n.Localizer) *guiController {
	configureFyneTranslations(string(tr.Locale()))
	a := app.NewWithID("org.goflasher.usbwriter")
	a.Settings().SetTheme(newReadableTheme())
	w := a.NewWindow(tr.T("window.title"))
	w.Resize(fyne.NewSize(720, 620))
	view := newApplicationView(tr, w)
	return &guiController{tr: tr, view: view, backend: newBackend(), machine: core.NewStateMachine()}
}

func newApplicationView(tr i18n.Localizer, w fyne.Window) *applicationView {
	v := &applicationView{window: w}
	v.deviceSelect = widget.NewSelect(nil, nil)
	v.deviceDetail = widget.NewLabel(tr.T("device.none"))
	v.deviceDetail.Wrapping = fyne.TextWrapWord
	v.imagePath = widget.NewEntry()
	v.imagePath.Disable()
	v.imageInfo = widget.NewLabel(tr.T("image.empty"))
	v.verifyCheck = widget.NewCheck(tr.T("option.verify"), nil)
	v.verifyCheck.SetChecked(true)
	v.ejectCheck = widget.NewCheck(tr.T("option.eject"), nil)
	v.ejectCheck.SetChecked(true)
	v.status = widget.NewLabel(tr.T("status.ready"))
	v.bar = widget.NewProgressBar()
	v.metrics = widget.NewLabel(tr.T("metrics.empty"))
	v.logs = widget.NewMultiLineEntry()
	v.logs.Disable()
	v.logPanel = widget.NewAccordion(widget.NewAccordionItem(tr.T("log.details"), v.logs))
	v.logPanel.CloseAll()
	v.start = widget.NewButton(tr.T("action.start"), nil)
	v.format = widget.NewButton(tr.T("action.format_fat32"), nil)
	v.format.Disable()
	v.choose = widget.NewButton(tr.T("action.choose"), nil)
	v.refresh = widget.NewButton(tr.T("action.rescan"), nil)
	v.copyError = widget.NewButton(tr.T("action.copy_error"), func() { w.Clipboard().SetContent(v.logs.Text) })
	v.copyError.Hide()
	v.copyLog = widget.NewButton(tr.T("action.copy_log"), func() { w.Clipboard().SetContent(v.logs.Text) })
	return v
}

func (c *guiController) bindActions() {
	c.view.refresh.OnTapped = c.refresh
	c.view.deviceSelect.OnChanged = c.selectDevice
	c.view.choose.OnTapped = c.chooseImage
	c.view.format.OnTapped = c.formatDevice
	c.view.start.OnTapped = c.startWrite
}

func (c *guiController) appendLog(message string) {
	line := time.Now().Format("15:04:05 ") + message
	fyne.Do(func() {
		c.logLines = append(c.logLines, line)
		const maxLogLines = 500
		if len(c.logLines) > maxLogLines {
			c.logLines = c.logLines[len(c.logLines)-maxLogLines:]
		}
		c.view.logs.SetText(strings.Join(c.logLines, "\n"))
	})
}

func deviceDisplay(d device.Device) string {
	return fmt.Sprintf("%s %s · %.1f GB · %s", d.Vendor, d.Model, float64(d.Size)/1e9, d.Path)
}

func (c *guiController) refresh() {
	go func() {
		list, err := c.backend.ListAllowedDevices(context.Background())
		if err != nil {
			fyne.Do(func() { c.view.status.SetText(c.tr.T("error.devices", err)) })
			return
		}
		fyne.Do(func() {
			c.devices = list
			options := make([]string, len(list))
			for i, d := range list {
				options[i] = deviceDisplay(d)
			}
			c.view.deviceSelect.Options = options
			c.view.deviceSelect.Refresh()
		})
		c.appendLog(c.tr.T("log.devices", len(list)))
	}()
}

func (c *guiController) selectDevice(value string) {
	for _, d := range c.devices {
		if deviceDisplay(d) != value {
			continue
		}
		c.selected = d
		c.view.deviceDetail.SetText(c.tr.T("device.details", d.Vendor, d.Model, float64(d.Size)/1e9, d.Path, d.Serial, localBool(c.tr, d.IsCardReader), localBool(c.tr, d.Mounted), d.PartitionCount))
		advanceSelection(c.machine, c.info.Path != "", core.ImageSelected, core.DeviceSelected)
		c.view.format.Enable()
		return
	}
}

func (c *guiController) chooseImage() {
	c.view.choose.Disable()
	openImage(c.view.window, c.tr.T("picker.image.title"), c.tr.T("picker.image.accept"), c.tr.T("action.cancel"), c.tr.T("filter.images"), func(path string, err error) {
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
	go func() {
		detected, err := image.Detect(path)
		if err != nil {
			fyne.Do(func() { dialog.ShowError(err, c.view.window) })
			return
		}
		fyne.Do(func() {
			c.info = detected
			c.view.imagePath.SetText(path)
			c.view.imageInfo.SetText(c.tr.T("image.details", c.info.Format, c.info.Compression, float64(c.info.CompressedSize)/(1<<20)))
			advanceSelection(c.machine, c.selected.Path != "", core.DeviceSelected, core.ImageSelected)
		})
		c.appendLog(c.tr.T("log.image", filepath.Base(path)))
	}()
}

func (c *guiController) lock(locked bool) {
	controls := []fyne.Disableable{c.view.deviceSelect, c.view.refresh, c.view.choose, c.view.verifyCheck, c.view.ejectCheck}
	for _, control := range controls {
		if locked {
			control.Disable()
		} else {
			control.Enable()
		}
	}
	if locked || c.selected.Path == "" {
		c.view.format.Disable()
	} else {
		c.view.format.Enable()
	}
}

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
	c.lock(true)
	c.view.status.SetText(c.tr.T("status.formatting"))
	c.view.bar.SetValue(0)
	c.view.bar.Show()
	c.view.metrics.SetText(c.tr.T("metrics.empty"))
	c.appendLog(c.tr.T("log.format.start", target.Path))
	updates := make(chan progress.Update, 100)
	go c.consumeFormatProgress(updates)
	go func() {
		err := formatter.FormatFAT32(context.Background(), target, "GOFLASHER", updates)
		close(updates)
		fyne.Do(func() { c.finishFormat(err) })
	}()
}

func (c *guiController) consumeFormatProgress(updates <-chan progress.Update) {
	for update := range updates {
		u := update
		fyne.Do(func() {
			c.view.bar.SetValue(overallProgress(u, false))
			c.view.status.SetText(c.tr.T("stage." + string(u.Stage)))
			c.view.metrics.SetText(c.tr.T("metrics.formatting", int(u.BytesProcessed)))
		})
	}
}

func (c *guiController) finishFormat(err error) {
	c.lock(false)
	c.view.bar.Show()
	if err != nil {
		c.view.status.SetText(c.tr.T("status.failed", err))
		c.appendLog(c.tr.T("log.error", err))
		c.view.copyError.Show()
		dialog.ShowError(err, c.view.window)
		return
	}
	c.view.status.SetText(c.tr.T("status.format.complete"))
	c.view.bar.SetValue(1)
	c.appendLog(c.tr.T("log.format.complete"))
	c.refresh()
}

func (c *guiController) startWrite() {
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
	c.lock(true)
	body := widget.NewLabel(c.tr.T("confirm.body", c.selected.Vendor, c.selected.Model, c.selected.Path, float64(c.selected.Size)/1e9, c.selected.Serial, filepath.Base(c.info.Path), float64(c.info.CompressedSize)/(1<<20)))
	body.Wrapping = fyne.TextWrapWord
	confirm := dialog.NewCustomConfirm(c.tr.T("confirm.title"), c.tr.T("confirm.accept"), c.tr.T("action.cancel"), body, c.confirmWrite, c.view.window)
	confirm.SetDismissText(c.tr.T("action.cancel"))
	confirm.Show()
}

func writingState(state core.State) bool {
	switch state {
	case core.Writing, core.Flushing, core.Verifying, core.Ejecting, core.Unmounting:
		return true
	default:
		return false
	}
}

func (c *guiController) cancelWrite() {
	if c.cancel != nil {
		c.cancel()
		c.view.status.SetText(c.tr.T("status.cancelling"))
		c.appendLog(c.tr.T("log.cancel"))
	}
}

func (c *guiController) confirmWrite(ok bool) {
	if !ok {
		_ = c.machine.Transition(core.Ready)
		c.lock(false)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.view.start.SetText(c.tr.T("action.cancel"))
	c.view.status.SetText(c.tr.T("status.preparing"))
	c.view.bar.Show()
	c.view.bar.SetValue(0)
	c.appendLog(c.tr.T("log.start"))
	updates := make(chan progress.Update, 32)
	go c.consumeWriteProgress(updates)
	go func() {
		result, err := (&core.Service{Backend: c.backend, State: c.machine}).Run(ctx, c.info, c.selected, core.RunOptions{Verify: c.view.verifyCheck.Checked, Eject: c.view.ejectCheck.Checked}, updates)
		close(updates)
		fyne.Do(func() { c.finishWrite(result, err) })
	}()
}

func (c *guiController) consumeWriteProgress(updates <-chan progress.Update) {
	for update := range updates {
		u := update
		fyne.Do(func() {
			c.view.status.SetText(c.tr.T("stage." + string(u.Stage)))
			c.view.bar.SetValue(overallProgress(u, c.view.verifyCheck.Checked))
			if u.TotalBytes > 0 {
				c.view.metrics.SetText(c.tr.T("metrics.progress", u.BytesPerSecond/(1<<20), float64(u.BytesProcessed)/(1<<20), u.ETA.Round(time.Second)))
			} else {
				c.view.metrics.SetText(c.tr.T("metrics.finalizing"))
			}
		})
	}
}

func (c *guiController) finishWrite(result core.RunResult, runErr error) {
	c.cancel = nil
	if runErr != nil {
		c.view.status.SetText(c.tr.T("status.failed", userError(c.tr, runErr)))
		c.appendLog(c.tr.T("log.error", runErr))
		c.view.copyError.Show()
		if c.machine.State() == core.Cancelled {
			c.view.status.SetText(c.tr.T("status.cancelled"))
		}
		c.view.start.SetText(c.tr.T("action.retry"))
	} else {
		c.view.status.SetText(c.tr.T("status.complete"))
		c.view.bar.SetValue(1)
		c.appendLog(c.tr.T("log.complete", result.BytesWritten, localBool(c.tr, result.Verified), localBool(c.tr, result.Ejected), result.Elapsed.Round(time.Second)))
		c.view.metrics.SetText(c.tr.T("metrics.complete", result.AverageBytesPerSecond/(1<<20), result.Elapsed.Round(time.Second)))
		c.view.start.SetText(c.tr.T("action.restart"))
	}
	c.lock(false)
}

func overallProgress(update progress.Update, verify bool) float64 {
	ratio := progressRatio(update)
	if verify {
		return verifyProgress(update.Stage, ratio)
	}
	return writeProgress(update.Stage, ratio)
}

func progressRatio(update progress.Update) float64 {
	if update.TotalBytes == 0 {
		return 0
	}
	return min(float64(update.BytesProcessed)/float64(update.TotalBytes), 1)
}

func verifyProgress(stage progress.Stage, ratio float64) float64 {
	switch stage {
	case progress.StageWriting, progress.StageDecompressWriting:
		return ratio * 0.45
	case progress.StageFlushing:
		return 0.45
	case progress.StageVerifying:
		return 0.45 + ratio*0.50
	case progress.StageEjecting:
		return 0.95
	default:
		return 0
	}
}

func writeProgress(stage progress.Stage, ratio float64) float64 {
	switch stage {
	case progress.StageFormatting:
		return ratio
	case progress.StageWriting, progress.StageDecompressWriting:
		return ratio * 0.90
	case progress.StageFlushing:
		return 0.90
	case progress.StageEjecting:
		return 0.95
	default:
		return 0
	}
}

func advanceSelection(machine *core.StateMachine, counterpartSelected bool, waitingState, selectedState core.State) {
	if counterpartSelected && machine.State() == waitingState {
		_ = machine.Transition(core.Ready)
	} else if !counterpartSelected && machine.State() == core.Idle {
		_ = machine.Transition(selectedState)
	}
}

func resetFinishedState(machine *core.StateMachine) {
	switch machine.State() {
	case core.Completed:
		_ = machine.Transition(core.Idle)
		_ = machine.Transition(core.ImageSelected)
		_ = machine.Transition(core.Ready)
	case core.Cancelled, core.Failed:
		_ = machine.Transition(core.Ready)
	}
}

func windowContent(tr i18n.Localizer, v *applicationView) *fyne.Container {
	deviceCard := widget.NewCard(tr.T("card.device"), "", container.NewVBox(container.NewBorder(nil, nil, nil, container.NewHBox(v.refresh, v.format), v.deviceSelect), v.deviceDetail))
	imageCard := widget.NewCard(tr.T("card.image"), "", container.NewBorder(nil, nil, nil, v.choose, v.imagePath))
	optionsCard := widget.NewCard(tr.T("card.options"), "", container.NewVBox(v.verifyCheck, v.ejectCheck))
	progressCard := widget.NewCard(tr.T("card.progress"), "", container.NewVBox(v.status, v.bar, v.metrics))
	actions := container.NewBorder(nil, nil, container.NewHBox(v.copyLog, v.copyError), v.start, v.logPanel)
	return container.NewVBox(deviceCard, imageCard, widget.NewCard(tr.T("card.image_info"), "", v.imageInfo), optionsCard, progressCard, actions)
}

func userError(tr i18n.Localizer, err error) string {
	if errors.Is(err, context.Canceled) {
		return tr.T("error.cancelled")
	}
	return err.Error()
}

func localBool(tr i18n.Localizer, value bool) string {
	if value {
		return tr.T("bool.true")
	}
	return tr.T("bool.false")
}
