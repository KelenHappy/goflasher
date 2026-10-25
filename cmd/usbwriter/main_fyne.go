//go:build fyne && (linux || windows || darwin)

package main

import (
	"context"
	_ "embed"
	"errors"
	"log/slog"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	core "github.com/goflasher/goflasher/internal/app"
	"github.com/goflasher/goflasher/internal/applog"
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
	controller.logEvent(slog.LevelInfo, "log.launched")
	controller.refresh()
	controller.watchDevices()
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
	start, format, choose, refresh, saveLog  *widget.Button
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
	logger            *slog.Logger
	history           *applog.History
	app               fyne.App
	operation         operation
	formatProgress    progress.Update
	refreshGeneration uint64
	refreshCancel     context.CancelFunc
	watchCancel       context.CancelFunc
	hotplug           *debouncer
	closing           bool
	shutdownDone      chan struct{}
	// installerRun records whether the confirmed run builds Windows installer
	// media, which reports progress through its own stages.
	installerRun bool
}

type operation uint8

const (
	operationNone operation = iota
	// operationFormatting is separate from the image-write state machine so the
	// two destructive workflows remain mutually exclusive.
	operationFormatting
)

const languagePreference = "language"

// appIcon is rendered from packaging/org.goflasher.usbwriter.svg. Plain
// go build does not apply FyneApp.toml, so the window icon is set explicitly.
//
//go:embed Icon.png
var appIcon []byte

func newGUIController(tr i18n.Localizer) *guiController {
	a := app.NewWithID("org.goflasher.usbwriter")
	a.SetIcon(fyne.NewStaticResource("Icon.png", appIcon))
	a.Settings().SetTheme(newReadableTheme(loadThemeMode(a.Preferences()), loadUIScale(a.Preferences())))
	if saved := a.Preferences().String(languagePreference); saved != "" {
		tr = i18n.New(saved)
	}
	configureFyneTranslations(string(tr.Locale()))
	w := a.NewWindow(tr.T("window.title"))
	w.Resize(baseWindowSize())
	view := newApplicationView(tr, w)
	c := &guiController{tr: tr, view: view, backend: newBackend(), machine: core.NewStateMachine(), app: a}
	c.initLogging()
	return c
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
	v.logPanel.Open(0)
	v.start = widget.NewButton(tr.T("action.start"), nil)
	v.format = widget.NewButton(tr.T("action.format_fat32"), nil)
	v.format.Disable()
	v.choose = widget.NewButton(tr.T("action.choose"), nil)
	v.refresh = widget.NewButton(tr.T("action.rescan"), nil)
	v.saveLog = widget.NewButton(tr.T("action.save_log"), nil)
	v.settings = widget.NewButton(tr.T("action.settings"), nil)
	return v
}

func (c *guiController) bindActions() {
	c.view.refresh.OnTapped = c.refresh
	c.view.deviceSelect.OnChanged = c.selectDevice
	c.view.choose.OnTapped = c.chooseImage
	c.view.saveLog.OnTapped = c.exportLog
	c.view.format.OnTapped = c.formatDevice
	c.view.start.OnTapped = c.startWrite
	c.view.settings.OnTapped = c.showSettings
	c.view.window.SetCloseIntercept(c.closeWindow)
}

func (c *guiController) closeWindow() {
	if c.cancel != nil {
		if !c.closing {
			c.closing = true
			c.shutdownDone = make(chan struct{})
		}
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
	c.stopWatchingDevices()
	c.view.window.SetCloseIntercept(nil)
	c.view.window.Close()
	if c.shutdownDone != nil {
		close(c.shutdownDone)
		c.shutdownDone = nil
	}
}

func isReadyState(state core.State) bool {
	switch state {
	case core.Idle, core.ImageSelected, core.DeviceSelected, core.Ready:
		return true
	default:
		return false
	}
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

// busy reports whether a format or write owns the target, either still
// starting up (operation) or already driving the state machine.
func (c *guiController) busy() bool {
	return c.operation != operationNone || writingState(c.machine.State())
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
	actions := container.NewBorder(nil, nil, v.saveLog, v.start, v.logPanel)
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
