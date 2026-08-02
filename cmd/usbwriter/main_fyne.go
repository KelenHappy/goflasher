//go:build linux && fyne

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
	"github.com/goflasher/goflasher/internal/filepicker"
	"github.com/goflasher/goflasher/internal/i18n"
	"github.com/goflasher/goflasher/internal/image"
	linuxbackend "github.com/goflasher/goflasher/internal/linux"
	"github.com/goflasher/goflasher/internal/progress"
)

func main() {
	tr := i18n.System()
	a := app.NewWithID("org.goflasher.usbwriter")
	w := a.NewWindow(tr.T("window.title"))
	w.Resize(fyne.NewSize(720, 620))
	backend := linuxbackend.NewBackend()
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
				if info.Path != "" {
					if machine.State() == core.ImageSelected {
						_ = machine.Transition(core.Ready)
					}
				} else if machine.State() == core.Idle {
					_ = machine.Transition(core.DeviceSelected)
				}
				break
			}
		}
	}
	var choose *widget.Button
	choose = widget.NewButton(tr.T("action.choose"), func() {
		choose.Disable()
		go func() {
			defer fyne.Do(choose.Enable)
			path, err := filepicker.OpenImage(tr.T("picker.image.title"), tr.T("picker.image.accept"), tr.T("filter.images"))
			if err != nil {
				fyne.Do(func() { dialog.ShowError(err, w) })
				return
			}
			if path == "" {
				return
			}
			detected, err := image.Detect(path)
			if err != nil {
				fyne.Do(func() { dialog.ShowError(err, w) })
				return
			}
			fyne.Do(func() {
				info = detected
				imagePath.SetText(path)
				imageInfo.SetText(tr.T("image.details", info.Format, info.Compression, float64(info.CompressedSize)/(1<<20)))
				if selected.Path != "" {
					if machine.State() == core.DeviceSelected {
						_ = machine.Transition(core.Ready)
					}
				} else if machine.State() == core.Idle {
					_ = machine.Transition(core.ImageSelected)
				}
			})
			appendLog(tr.T("log.image", filepath.Base(path)))
		}()
	})
	lock := func(v bool) {
		if v {
			deviceSelect.Disable()
			refreshButton.Disable()
			choose.Disable()
			verifyCheck.Disable()
			ejectCheck.Disable()
		} else {
			deviceSelect.Enable()
			refreshButton.Enable()
			choose.Enable()
			verifyCheck.Enable()
			ejectCheck.Enable()
		}
	}
	start.OnTapped = func() {
		if machine.State() == core.Completed {
			_ = machine.Transition(core.Idle)
			_ = machine.Transition(core.ImageSelected)
			_ = machine.Transition(core.Ready)
		}
		if machine.State() == core.Cancelled || machine.State() == core.Failed {
			_ = machine.Transition(core.Ready)
		}
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
			appendLog(tr.T("log.start"))
			updates := make(chan progress.Update, 32)
			go func() {
				for u := range updates {
					u := u
					fyne.Do(func() {
						status.SetText(tr.T("stage." + string(u.Stage)))
						if u.TotalBytes > 0 {
							bar.SetValue(float64(u.BytesProcessed) / float64(u.TotalBytes))
						}
						metrics.SetText(tr.T("metrics.progress", u.BytesPerSecond/(1<<20), float64(u.BytesProcessed)/(1<<20), u.ETA.Round(time.Second)))
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
	content := container.NewVBox(widget.NewCard(tr.T("card.device"), "", container.NewVBox(container.NewBorder(nil, nil, nil, refreshButton, deviceSelect), deviceDetail)), widget.NewCard(tr.T("card.image"), "", container.NewBorder(nil, nil, nil, choose, imagePath)), widget.NewCard(tr.T("card.image_info"), "", imageInfo), widget.NewCard(tr.T("card.options"), "", container.NewVBox(verifyCheck, ejectCheck)), widget.NewCard(tr.T("card.progress"), "", container.NewVBox(status, bar, metrics)), container.NewBorder(nil, nil, container.NewHBox(copyLog, copyError), start, logPanel))
	w.SetContent(container.NewVScroll(content))
	appendLog(tr.T("log.launched"))
	refresh()
	w.ShowAndRun()
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
