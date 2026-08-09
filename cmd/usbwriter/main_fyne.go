//go:build fyne && (linux || windows || darwin)

package main

import (
	"context"
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
	configureFyneTranslations(string(tr.Locale()))
	a := app.NewWithID("org.goflasher.usbwriter")
	a.Settings().SetTheme(newReadableTheme())
	w := a.NewWindow(tr.T("window.title"))
	w.Resize(fyne.NewSize(720, 620))
	backend := newBackend()
	machine := core.NewStateMachine()
	var devices []device.Device
	var selected device.Device
	var info image.Info
	var cancel context.CancelFunc
	deviceSelect := widget.NewSelect(nil, nil)
	deviceDetail := widget.NewLabel(tr.T("device.none"))
	deviceDetail.Wrapping = fyne.TextWrapWord
	imagePath := widget.NewEntry()
	imagePath.Disable()
	imageInfo := widget.NewLabel(tr.T("image.empty"))
	verifyCheck := widget.NewCheck(tr.T("option.verify"), nil)
	verifyCheck.SetChecked(true)
	ejectCheck := widget.NewCheck(tr.T("option.eject"), nil)
	ejectCheck.SetChecked(true)
	status := widget.NewLabel(tr.T("status.ready"))
	bar := widget.NewProgressBar()

	metrics := widget.NewLabel(tr.T("metrics.empty"))
	logs := widget.NewMultiLineEntry()
	logs.Disable()
	logItem := widget.NewAccordionItem(tr.T("log.details"), logs)
	logPanel := widget.NewAccordion(logItem)
	logPanel.CloseAll()
	start := widget.NewButton(tr.T("action.start"), nil)
	format := widget.NewButton(tr.T("action.format_fat32"), nil)
	format.Disable()
	copyError := widget.NewButton(tr.T("action.copy_error"), func() { w.Clipboard().SetContent(logs.Text) })
	copyError.Hide()
	copyLog := widget.NewButton(tr.T("action.copy_log"), func() { w.Clipboard().SetContent(logs.Text) })
	var logLines []string
	const maxLogLines = 500
	appendLog := func(message string) {
		line := time.Now().Format("15:04:05 ") + message
		fyne.Do(func() {
			logLines = append(logLines, line)
			if len(logLines) > maxLogLines {
				logLines = logLines[len(logLines)-maxLogLines:]
			}
			logs.SetText(strings.Join(logLines, "\n"))
		})
	}
	formatDevice := func(d device.Device) string {
		return fmt.Sprintf("%s %s · %.1f GB · %s", d.Vendor, d.Model, float64(d.Size)/1e9, d.Path)
	}
	refresh := func() {
		go func() {
			list, err := backend.ListAllowedDevices(context.Background())
			if err != nil {
				fyne.Do(func() { status.SetText(tr.T("error.devices", err)) })
				return
			}
			fyne.Do(func() {
				devices = list
				options := make([]string, len(list))
				for i, d := range list {
					options[i] = formatDevice(d)
				}
				deviceSelect.Options = options
				deviceSelect.Refresh()
			})
			appendLog(tr.T("log.devices", len(list)))
		}()
	}
	refreshButton := widget.NewButton(tr.T("action.rescan"), refresh)
	deviceSelect.OnChanged = func(value string) {
		for _, d := range devices {
			if formatDevice(d) == value {
				selected = d
				deviceDetail.SetText(tr.T("device.details", d.Vendor, d.Model, float64(d.Size)/1e9, d.Path, d.Serial, localBool(tr, d.IsCardReader), localBool(tr, d.Mounted), d.PartitionCount))
				advanceSelection(machine, info.Path != "", core.ImageSelected, core.DeviceSelected)
				format.Enable()
				break
			}
		}
	}
	var choose *widget.Button
	choose = widget.NewButton(tr.T("action.choose"), func() {
		choose.Disable()
		openImage(w, tr.T("picker.image.title"), tr.T("picker.image.accept"), tr.T("action.cancel"), tr.T("filter.images"), func(path string, err error) {
			choose.Enable()
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			if path == "" {
				return
			}
			go func() {
				detected, err := image.Detect(path)
				if err != nil {
					fyne.Do(func() { dialog.ShowError(err, w) })
					return
				}
				fyne.Do(func() {
					info = detected
					imagePath.SetText(path)
					imageInfo.SetText(tr.T("image.details", info.Format, info.Compression, float64(info.CompressedSize)/(1<<20)))
					advanceSelection(machine, selected.Path != "", core.DeviceSelected, core.ImageSelected)
				})
				appendLog(tr.T("log.image", filepath.Base(path)))
			}()
		})
	})
	lock := func(v bool) {
		if v {
			deviceSelect.Disable()
			refreshButton.Disable()
			choose.Disable()
			format.Disable()
			verifyCheck.Disable()
			ejectCheck.Disable()
		} else {
			deviceSelect.Enable()
			refreshButton.Enable()
			choose.Enable()
			if selected.Path != "" {
				format.Enable()
			}
			verifyCheck.Enable()
			ejectCheck.Enable()
		}
	}
	format.OnTapped = func() {
		formatter, ok := backend.(device.FAT32Formatter)
		if !ok || selected.Path == "" {
			dialog.ShowInformation(tr.T("dialog.not_ready.title"), tr.T("dialog.format.not_ready"), w)
			return
		}
		body := widget.NewLabel(tr.T("format.confirm.body", selected.Vendor, selected.Model, selected.Path, float64(selected.Size)/1e9, selected.Serial))
		body.Wrapping = fyne.TextWrapWord
		confirm := dialog.NewCustomConfirm(tr.T("format.confirm.title"), tr.T("format.confirm.accept"), tr.T("action.cancel"), body, func(ok bool) {
			if !ok {
				return
			}
			lock(true)
			status.SetText(tr.T("status.formatting"))
			bar.SetValue(0)
			bar.Show()
			metrics.SetText(tr.T("metrics.empty"))
			appendLog(tr.T("log.format.start", selected.Path))
			updates := make(chan progress.Update, 100)
			go func() {
				for u := range updates {
					u := u
					fyne.Do(func() {
						bar.SetValue(overallProgress(u, false))
						status.SetText(tr.T("stage." + string(u.Stage)))
						metrics.SetText(tr.T("metrics.formatting", int(u.BytesProcessed)))
					})
				}
			}()
			go func(target device.Device) {
				err := formatter.FormatFAT32(context.Background(), target, "GOFLASHER", updates)
				close(updates)
				fyne.Do(func() {
					lock(false)
					bar.Show()
					if err != nil {
						status.SetText(tr.T("status.failed", err))
						appendLog(tr.T("log.error", err))
						copyError.Show()
						dialog.ShowError(err, w)
						return
					}
					status.SetText(tr.T("status.format.complete"))
					bar.SetValue(1)
					appendLog(tr.T("log.format.complete"))
					refresh()
				})
			}(selected)
		}, w)
		confirm.SetDismissText(tr.T("action.cancel"))
		confirm.Show()
	}
	start.OnTapped = func() {
		resetFinishedState(machine)
		switch machine.State() {
		case core.Writing, core.Flushing, core.Verifying, core.Ejecting, core.Unmounting:
			if cancel != nil {
				cancel()
				status.SetText(tr.T("status.cancelling"))
				appendLog(tr.T("log.cancel"))
			}
			return
		}
		if machine.State() != core.Ready {
			dialog.ShowInformation(tr.T("dialog.not_ready.title"), tr.T("dialog.not_ready.body"), w)
			return
		}
		_ = machine.Transition(core.Confirming)
		lock(true)
		body := widget.NewLabel(tr.T("confirm.body", selected.Vendor, selected.Model, selected.Path, float64(selected.Size)/1e9, selected.Serial, filepath.Base(info.Path), float64(info.CompressedSize)/(1<<20)))
		body.Wrapping = fyne.TextWrapWord
		confirm := dialog.NewCustomConfirm(tr.T("confirm.title"), tr.T("confirm.accept"), tr.T("action.cancel"), body, func(ok bool) {
			if !ok {
				_ = machine.Transition(core.Ready)
				lock(false)
				return
			}
			ctx, c := context.WithCancel(context.Background())
			cancel = c
			start.SetText(tr.T("action.cancel"))
			status.SetText(tr.T("status.preparing"))

			bar.Show()
			bar.SetValue(0)
			appendLog(tr.T("log.start"))
			updates := make(chan progress.Update, 32)
			go func() {
				for u := range updates {
					u := u
					fyne.Do(func() {
						status.SetText(tr.T("stage." + string(u.Stage)))
						bar.SetValue(overallProgress(u, verifyCheck.Checked))
						if u.TotalBytes > 0 {
							metrics.SetText(tr.T("metrics.progress", u.BytesPerSecond/(1<<20), float64(u.BytesProcessed)/(1<<20), u.ETA.Round(time.Second)))
						} else {
							metrics.SetText(tr.T("metrics.finalizing"))
						}
					})
				}
			}()
			go func() {
				result, runErr := (&core.Service{Backend: backend, State: machine}).Run(ctx, info, selected, core.RunOptions{Verify: verifyCheck.Checked, Eject: ejectCheck.Checked}, updates)
				close(updates)
				fyne.Do(func() {
					cancel = nil
					if runErr != nil {
						status.SetText(tr.T("status.failed", userError(tr, runErr)))
						appendLog(tr.T("log.error", runErr))
						copyError.Show()
						if machine.State() == core.Cancelled {
							status.SetText(tr.T("status.cancelled"))
						}
						start.SetText(tr.T("action.retry"))
					} else {
						status.SetText(tr.T("status.complete"))
						bar.SetValue(1)
						appendLog(tr.T("log.complete", result.BytesWritten, localBool(tr, result.Verified), localBool(tr, result.Ejected), result.Elapsed.Round(time.Second)))
						metrics.SetText(tr.T("metrics.complete", result.AverageBytesPerSecond/(1<<20), result.Elapsed.Round(time.Second)))
						start.SetText(tr.T("action.restart"))
					}
					lock(false)
				})
			}()
		}, w)
		confirm.SetDismissText(tr.T("action.cancel"))
		confirm.Show()
	}
	content := windowContent(tr, deviceSelect, deviceDetail, refreshButton, format, choose, imagePath, imageInfo, verifyCheck, ejectCheck, status, bar, metrics, copyLog, copyError, start, logPanel)
	w.SetContent(container.NewVScroll(content))
	appendLog(tr.T("log.launched"))
	refresh()
	w.ShowAndRun()
}

func overallProgress(update progress.Update, verify bool) float64 {
	ratio := 0.0
	if update.TotalBytes > 0 {
		ratio = float64(update.BytesProcessed) / float64(update.TotalBytes)
		if ratio > 1 {
			ratio = 1
		}
	}
	if verify {
		switch update.Stage {
		case progress.StageWriting, progress.StageDecompressWriting:
			return ratio * 0.45
		case progress.StageFlushing:
			return 0.45
		case progress.StageVerifying:
			return 0.45 + ratio*0.50
		case progress.StageEjecting:
			return 0.95
		}
	}
	switch update.Stage {
	case progress.StageFormatting:
		return ratio
	case progress.StageWriting, progress.StageDecompressWriting:
		return ratio * 0.90
	case progress.StageFlushing:
		return 0.90
	case progress.StageEjecting:
		return 0.95
	}
	return 0
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

func windowContent(tr i18n.Localizer, deviceSelect *widget.Select, deviceDetail *widget.Label, refreshButton, format, choose *widget.Button, imagePath *widget.Entry, imageInfo *widget.Label, verifyCheck, ejectCheck *widget.Check, status *widget.Label, bar *widget.ProgressBar, metrics *widget.Label, copyLog, copyError, start *widget.Button, logPanel *widget.Accordion) *fyne.Container {
	deviceCard := widget.NewCard(tr.T("card.device"), "", container.NewVBox(container.NewBorder(nil, nil, nil, container.NewHBox(refreshButton, format), deviceSelect), deviceDetail))
	imageCard := widget.NewCard(tr.T("card.image"), "", container.NewBorder(nil, nil, nil, choose, imagePath))
	optionsCard := widget.NewCard(tr.T("card.options"), "", container.NewVBox(verifyCheck, ejectCheck))
	progressCard := widget.NewCard(tr.T("card.progress"), "", container.NewVBox(status, bar, metrics))
	actions := container.NewBorder(nil, nil, container.NewHBox(copyLog, copyError), start, logPanel)
	return container.NewVBox(deviceCard, imageCard, widget.NewCard(tr.T("card.image_info"), "", imageInfo), optionsCard, progressCard, actions)
}

func userError(tr i18n.Localizer, err error) string {
	if err == context.Canceled {
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
