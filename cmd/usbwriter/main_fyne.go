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
	"github.com/goflasher/goflasher/internal/image"
	linuxbackend "github.com/goflasher/goflasher/internal/linux"
	"github.com/goflasher/goflasher/internal/progress"
)

func main() {
	a := app.NewWithID("org.goflasher.usbwriter")
	w := a.NewWindow("Linux USB Writer")
	w.Resize(fyne.NewSize(720, 620))
	backend := linuxbackend.NewBackend()
	machine := core.NewStateMachine()
	var devices []device.Device
	var selected device.Device
	var info image.Info
	var cancel context.CancelFunc
	deviceSelect := widget.NewSelect(nil, nil)
	deviceDetail := widget.NewLabel("未選擇裝置")
	deviceDetail.Wrapping = fyne.TextWrapWord
	imagePath := widget.NewEntry()
	imagePath.Disable()
	imageInfo := widget.NewLabel("映像格式：—\n映像大小：—\nSHA-256：未驗證")
	verifyCheck := widget.NewCheck("寫入後驗證", nil)
	verifyCheck.SetChecked(true)
	ejectCheck := widget.NewCheck("完成後安全退出", nil)
	ejectCheck.SetChecked(true)
	status := widget.NewLabel("準備就緒")
	bar := widget.NewProgressBar()
	metrics := widget.NewLabel("速度：—        已寫入：—        剩餘：—")
	logs := widget.NewMultiLineEntry()
	logs.Disable()
	logItem := widget.NewAccordionItem("詳細記錄", logs)
	logPanel := widget.NewAccordion(logItem)
	logPanel.CloseAll()
	start := widget.NewButton("開始", nil)
	copyError := widget.NewButton("複製錯誤資訊", func() { w.Clipboard().SetContent(logs.Text) })
	copyError.Hide()
	copyLog := widget.NewButton("複製記錄", func() { w.Clipboard().SetContent(logs.Text) })
	appendLog := func(message string) {
		line := time.Now().Format("15:04:05 ") + message
		if logs.Text != "" {
			logs.SetText(logs.Text + "\n" + line)
		} else {
			logs.SetText(line)
		}
	}
	formatDevice := func(d device.Device) string {
		return fmt.Sprintf("%s %s · %.1f GB · %s", d.Vendor, d.Model, float64(d.Size)/1e9, d.Path)
	}
	refresh := func() {
		list, err := backend.ListAllowedDevices(context.Background())
		if err != nil {
			status.SetText("無法讀取裝置：" + err.Error())
			return
		}
		devices = list
		options := make([]string, len(list))
		for i, d := range list {
			options[i] = formatDevice(d)
		}
		deviceSelect.Options = options
		deviceSelect.Refresh()
		appendLog(fmt.Sprintf("找到 %d 個允許的 USB 裝置", len(list)))
	}
	refreshButton := widget.NewButton("重新掃描", refresh)
	deviceSelect.OnChanged = func(value string) {
		for _, d := range devices {
			if formatDevice(d) == value {
				selected = d
				deviceDetail.SetText(fmt.Sprintf("%s %s\n%.1f GB · %s\nSerial: %s · USB · 讀卡器: %v · 已掛載: %v · %d 個分割區", d.Vendor, d.Model, float64(d.Size)/1e9, d.Path, d.Serial, d.IsCardReader, d.Mounted, d.PartitionCount))
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
	choose := widget.NewButton("選擇", func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil {
				status.SetText(err.Error())
				return
			}
			if r == nil {
				return
			}
			path := r.URI().Path()
			_ = r.Close()
			detected, err := image.Detect(path)
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			info = detected
			imagePath.SetText(path)
			imageInfo.SetText(fmt.Sprintf("映像格式：%s\n壓縮：%s\n檔案大小：%.1f MB\nSHA-256：檢查時產生", info.Format, info.Compression, float64(info.CompressedSize)/(1<<20)))
			if selected.Path != "" {
				if machine.State() == core.DeviceSelected {
					_ = machine.Transition(core.Ready)
				}
			} else if machine.State() == core.Idle {
				_ = machine.Transition(core.ImageSelected)
			}
			appendLog("已選擇映像 " + filepath.Base(path))
		}, w)
		fd.SetFilter(imageFilter{})
		fd.Show()
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
				status.SetText("正在取消…")
				appendLog("使用者要求取消")
			}
			return
		}
		if machine.State() != core.Ready {
			dialog.ShowInformation("尚未準備", "請先選擇映像與 USB 裝置。", w)
			return
		}
		_ = machine.Transition(core.Confirming)
		lock(true)
		body := widget.NewLabel(fmt.Sprintf("即將清除以下裝置的所有資料\n\n%s %s\n%s\n%.1f GB\nSerial: %s\n\n映像：%s\n%.1f MB\n\n此操作無法復原。", selected.Vendor, selected.Model, selected.Path, float64(selected.Size)/1e9, selected.Serial, filepath.Base(info.Path), float64(info.CompressedSize)/(1<<20)))
		body.Wrapping = fyne.TextWrapWord
		confirm := dialog.NewCustomConfirm("即將清除裝置資料", "確認並寫入", "取消", body, func(ok bool) {
			if !ok {
				_ = machine.Transition(core.Ready)
				lock(false)
				return
			}
			ctx, c := context.WithCancel(context.Background())
			cancel = c
			start.SetText("取消")
			status.SetText("正在準備映像…")
			appendLog("開始安全寫入流程")
			updates := make(chan progress.Update, 32)
			go func() {
				for u := range updates {
					u := u
					fyne.Do(func() {
						status.SetText(string(u.Stage))
						if u.TotalBytes > 0 {
							bar.SetValue(float64(u.BytesProcessed) / float64(u.TotalBytes))
						}
						metrics.SetText(fmt.Sprintf("速度：%.1f MiB/s        已處理：%.1f MiB        剩餘：%s", u.BytesPerSecond/(1<<20), float64(u.BytesProcessed)/(1<<20), u.ETA.Round(time.Second)))
					})
				}
			}()
			go func() {
				result, runErr := (&core.Service{Backend: backend, State: machine}).Run(ctx, info, selected, core.RunOptions{Verify: verifyCheck.Checked, Eject: ejectCheck.Checked}, updates)
				close(updates)
				fyne.Do(func() {
					cancel = nil
					if runErr != nil {
						status.SetText("失敗：" + userError(runErr))
						appendLog("錯誤：" + runErr.Error())
						copyError.Show()
						if machine.State() == core.Cancelled {
							status.SetText("已取消；裝置內容可能已損毀")
						}
						start.SetText("重試")
					} else {
						status.SetText("寫入完成")
						bar.SetValue(1)
						appendLog(fmt.Sprintf("完成：%d bytes，驗證=%v，退出=%v，耗時=%s", result.BytesWritten, result.Verified, result.Ejected, result.Elapsed.Round(time.Second)))
						metrics.SetText(fmt.Sprintf("平均速度：%.1f MiB/s        總耗時：%s", result.AverageBytesPerSecond/(1<<20), result.Elapsed.Round(time.Second)))
						start.SetText("重新開始")
					}
					lock(false)
				})
			}()
		}, w)
		confirm.SetDismissText("取消")
		confirm.Show()
	}
	content := container.NewVBox(widget.NewCard("USB 裝置", "", container.NewVBox(container.NewBorder(nil, nil, nil, refreshButton, deviceSelect), deviceDetail)), widget.NewCard("映像檔案", "", container.NewBorder(nil, nil, nil, choose, imagePath)), widget.NewCard("映像資訊", "", imageInfo), widget.NewCard("寫入選項", "", container.NewVBox(verifyCheck, ejectCheck)), widget.NewCard("狀態與進度", "", container.NewVBox(status, bar, metrics)), container.NewBorder(nil, nil, container.NewHBox(copyLog, copyError), start, logPanel))
	w.SetContent(container.NewVScroll(content))
	appendLog("GoFlasher 啟動（無 telemetry）")
	refresh()
	w.ShowAndRun()
}

type imageFilter struct{}

func (imageFilter) Matches(uri fyne.URI) bool {
	n := strings.ToLower(uri.Name())
	for _, s := range []string{".iso", ".img", ".raw", ".img.gz", ".iso.gz", ".img.xz", ".iso.xz"} {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}
func (imageFilter) Name() string { return "USB images" }
func userError(err error) string {
	if err == context.Canceled {
		return "操作已取消"
	}
	return err.Error()
}
