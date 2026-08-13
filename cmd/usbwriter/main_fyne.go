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
	tr = controller.tr
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
	settings                                 *widget.Button
	deviceCard, imageCard, imageInfoCard     *widget.Card
	optionsCard, progressCard                *widget.Card
}

type guiController struct {
	tr                i18n.Localizer
	view              *applicationView
	backend           device.Backend
	machine           *core.StateMachine
	devices           []device.Device
	selected          device.Device
	info              image.Info
	cancel            context.CancelFunc
	logLines          []string
	app               fyne.App
	operation         operation
	formatProgress    progress.Update
	refreshGeneration uint64
	refreshCancel     context.CancelFunc
	closing           bool
}

type operation uint8

const (
	operationNone operation = iota
	// operationFormatting is separate from the image-write state machine so the
	// two destructive workflows remain mutually exclusive.
	operationFormatting
)

const languagePreference = "language"

func newGUIController(tr i18n.Localizer) *guiController {
	a := app.NewWithID("org.goflasher.usbwriter")
	a.Settings().SetTheme(newReadableTheme())
	if saved := a.Preferences().String(languagePreference); saved != "" {
		tr = i18n.New(saved)
	}
	configureFyneTranslations(string(tr.Locale()))
	w := a.NewWindow(tr.T("window.title"))
	w.Resize(fyne.NewSize(720, 620))
	view := newApplicationView(tr, w)
	return &guiController{tr: tr, view: view, backend: newBackend(), machine: core.NewStateMachine(), app: a}
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
	v.settings = widget.NewButton(tr.T("action.settings"), nil)
	return v
}

func (c *guiController) bindActions() {
	c.view.refresh.OnTapped = c.refresh
	c.view.deviceSelect.OnChanged = c.selectDevice
	c.view.choose.OnTapped = c.chooseImage
	c.view.format.OnTapped = c.formatDevice
	c.view.start.OnTapped = c.startWrite
	c.view.settings.OnTapped = c.showSettings
	c.view.window.SetCloseIntercept(c.closeWindow)
}

func (c *guiController) closeWindow() {
	if c.cancel != nil {
		c.closing = true
		c.cancel()
		c.view.status.SetText(c.tr.T("status.cancelling"))
		return
	}
	c.closeNow()
}

func (c *guiController) closeNow() {
	if c.refreshCancel != nil {
		c.refreshCancel()
		c.refreshCancel = nil
	}
	c.view.window.SetCloseIntercept(nil)
	c.view.window.Close()
}

func (c *guiController) showSettings() {
	languages := []string{"English", "繁體中文", "简体中文", "日本語"}
	locales := map[string]i18n.Locale{"English": i18n.English, "繁體中文": i18n.TraditionalChinese, "简体中文": i18n.SimplifiedChinese, "日本語": i18n.Japanese}
	selected := widget.NewSelect(languages, func(name string) {
		locale := locales[name]
		c.app.Preferences().SetString(languagePreference, string(locale))
		c.setLanguage(locale)
	})
	for name, locale := range locales {
		if locale == c.tr.Locale() {
			selected.SetSelected(name)
			break
		}
	}
	content := container.NewBorder(nil, nil, widget.NewLabel(c.tr.T("settings.language")), nil, selected)
	dialog.NewCustom(c.tr.T("settings.title"), c.tr.T("settings.close"), content, c.view.window).Show()
}

func (c *guiController) setLanguage(locale i18n.Locale) {
	c.tr = i18n.New(string(locale))
	configureFyneTranslations(string(locale))
	tr := c.tr
	c.view.window.SetTitle(tr.T("window.title"))
	c.view.verifyCheck.SetText(tr.T("option.verify"))
	c.view.ejectCheck.SetText(tr.T("option.eject"))
	c.view.start.SetText(tr.T(startActionKey(c.machine.State())))
	c.view.format.SetText(tr.T("action.format_fat32"))
	c.view.choose.SetText(tr.T("action.choose"))
	c.view.refresh.SetText(tr.T("action.rescan"))
	c.view.copyError.SetText(tr.T("action.copy_error"))
	c.view.copyLog.SetText(tr.T("action.copy_log"))
	c.view.settings.SetText(tr.T("action.settings"))
	c.view.logPanel.Items[0].Title = tr.T("log.details")
	c.view.deviceCard.SetTitle(tr.T("card.device"))
	c.view.imageCard.SetTitle(tr.T("card.image"))
	c.view.imageInfoCard.SetTitle(tr.T("card.image_info"))
	c.view.optionsCard.SetTitle(tr.T("card.options"))
	c.view.progressCard.SetTitle(tr.T("card.progress"))
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
	if c.operation == operationFormatting {
		c.view.status.SetText(tr.T("status.formatting"))
		c.view.metrics.SetText(tr.T("metrics.empty"))
		if c.formatProgress.Stage != "" {
			c.view.status.SetText(tr.T("stage." + string(c.formatProgress.Stage)))
			c.view.metrics.SetText(tr.T("metrics.formatting", int(c.formatProgress.BytesProcessed)))
		}
	} else {
		switch c.machine.State() {
		case core.Idle, core.ImageSelected, core.DeviceSelected, core.Ready:
			c.view.status.SetText(tr.T("status.ready"))
			c.view.metrics.SetText(tr.T("metrics.empty"))
		}
	}
	c.view.window.Content().Refresh()
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
	// A generation makes the most recently requested scan authoritative; device
	// enumeration can otherwise complete out of order.
	c.refreshGeneration++
	generation := c.refreshGeneration
	if c.refreshCancel != nil {
		c.refreshCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.refreshCancel = cancel
	go func() {
		list, err := c.backend.ListAllowedDevices(ctx)
		if err != nil {
			fyne.Do(func() {
				if generation == c.refreshGeneration {
					c.refreshCancel = nil
				}
				if generation == c.refreshGeneration && !errors.Is(err, context.Canceled) {
					c.view.status.SetText(c.tr.T("error.devices", err))
				}
			})
			return
		}
		fyne.Do(func() {
			if generation != c.refreshGeneration {
				return
			}
			c.devices = list
			options := make([]string, len(list))
			for i, d := range list {
				options[i] = deviceDisplay(d)
			}
			c.view.deviceSelect.Options = options
			c.view.deviceSelect.Refresh()
			c.appendLog(c.tr.T("log.devices", len(list)))
			c.refreshCancel = nil
		})
	}()
}

func (c *guiController) selectDevice(value string) {
	for _, d := range c.devices {
		if deviceDisplay(d) != value {
			continue
		}
		c.selected = d
		c.updateDeviceDetail()
		advanceSelection(c.machine, c.info.Path != "", core.ImageSelected, core.DeviceSelected)
		c.view.format.Enable()
		return
	}
}

// updateDeviceDetail deliberately avoids selectDevice: changing presentation
// must not alter the state machine or unlock formatting during an operation.
func (c *guiController) updateDeviceDetail() {
	d := c.selected
	c.view.deviceDetail.SetText(c.tr.T("device.details", d.Vendor, d.Model, float64(d.Size)/1e9, d.Path, d.Serial, localBool(c.tr, d.IsCardReader), localBool(c.tr, d.Mounted), d.PartitionCount))
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
			c.appendLog(c.tr.T("log.image", filepath.Base(path)))
		})
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
	if c.operation != operationNone || writingState(c.machine.State()) {
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
	c.appendLog(c.tr.T("log.format.start", target.Path))
	updates := make(chan progress.Update, 100)
	progressDone := make(chan struct{})
	go func() {
		c.consumeFormatProgress(updates)
		close(progressDone)
	}()
	go func() {
		err := formatter.FormatFAT32(ctx, target, "GOFLASHER", updates)
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
	progressDone := make(chan struct{})
	go func() {
		c.consumeWriteProgress(updates)
		close(progressDone)
	}()
	go func() {
		result, err := (&core.Service{Backend: c.backend, State: c.machine}).Run(ctx, c.info, c.selected, core.RunOptions{Verify: c.view.verifyCheck.Checked, Eject: c.view.ejectCheck.Checked}, updates)
		close(updates)
		<-progressDone
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
	if c.closing {
		c.closeNow()
		return
	}
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
	v.deviceCard = widget.NewCard(tr.T("card.device"), "", container.NewVBox(container.NewBorder(nil, nil, nil, container.NewHBox(v.refresh, v.format), v.deviceSelect), v.deviceDetail))
	v.imageCard = widget.NewCard(tr.T("card.image"), "", container.NewBorder(nil, nil, nil, v.choose, v.imagePath))
	v.imageInfoCard = widget.NewCard(tr.T("card.image_info"), "", v.imageInfo)
	v.optionsCard = widget.NewCard(tr.T("card.options"), "", container.NewVBox(v.verifyCheck, v.ejectCheck))
	v.progressCard = widget.NewCard(tr.T("card.progress"), "", container.NewVBox(v.status, v.bar, v.metrics))
	actions := container.NewBorder(nil, nil, container.NewHBox(v.copyLog, v.copyError), v.start, v.logPanel)
	return container.NewVBox(container.NewHBox(v.settings), v.deviceCard, v.imageCard, v.imageInfoCard, v.optionsCard, v.progressCard, actions)
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
