// Package i18n provides the user-interface message catalog and locale
// selection used by GoFlasher's launchers.
package i18n

import (
	"fmt"
	"os"
	"strings"
)

// Locale identifies a language supported by GoFlasher.
type Locale string

const (
	English            Locale = "en"
	TraditionalChinese Locale = "zh-TW"
)

// Localizer resolves message IDs using a selected locale.
type Localizer struct{ locale Locale }

// New returns a localizer for language. Unknown languages fall back to English.
// POSIX locale suffixes such as ".UTF-8" and region separators are accepted.
func New(language string) Localizer { return Localizer{locale: ParseLocale(language)} }

// System selects GOFLASHER_LANG when set, followed by LC_ALL, LC_MESSAGES and
// LANG. This gives users an application-specific override without changing
// their desktop locale.
func System() Localizer {
	for _, name := range []string{"GOFLASHER_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(name); value != "" {
			return New(value)
		}
	}
	return New("")
}

// ParseLocale normalizes a locale name to one of the supported locales.
func ParseLocale(language string) Locale {
	language = strings.ToLower(strings.TrimSpace(language))
	if i := strings.IndexAny(language, ".@"); i >= 0 {
		language = language[:i]
	}
	language = strings.ReplaceAll(language, "_", "-")
	if language == "zh" || strings.HasPrefix(language, "zh-") {
		return TraditionalChinese
	}
	return English
}

// Locale reports the resolved locale.
func (l Localizer) Locale() Locale { return l.locale }

// T translates id and applies fmt.Sprintf arguments. Missing translations use
// the English catalog; unknown IDs are returned verbatim to aid diagnostics.
func (l Localizer) T(id string, args ...any) string {
	message, ok := catalogs[l.locale][id]
	if !ok {
		message, ok = catalogs[English][id]
	}
	if !ok {
		message = id
	}
	if len(args) == 0 {
		return message
	}
	return fmt.Sprintf(message, args...)
}

var catalogs = map[Locale]map[string]string{
	English: {
		"launcher":     "GoFlasher GUI source is available with the 'fyne' build tag. Install Fyne build dependencies, then run: go run -tags fyne ./cmd/usbwriter",
		"window.title": "GoFlasher USB Writer", "device.none": "No device selected",
		"image.empty":   "Image format: —\nImage size: —\nSHA-256: not verified",
		"option.verify": "Verify after writing", "option.eject": "Safely eject when finished",
		"status.ready": "Ready", "metrics.empty": "Speed: —        Written: —        Remaining: —",
		"log.details": "Detailed log", "action.start": "Start", "action.copy_error": "Copy error details",
		"action.copy_log": "Copy log", "action.rescan": "Rescan", "action.choose": "Choose",
		"error.devices": "Cannot read devices: %v", "log.devices": "Found %d allowed USB devices",
		"device.details": "%s %s\n%.1f GB · %s\nSerial: %s · USB · Card reader: %s · Mounted: %s · %d partitions",
		"image.details":  "Image format: %s\nCompression: %s\nFile size: %.1f MB\nSHA-256: generated during verification",
		"log.image":      "Selected image %s", "status.cancelling": "Cancelling…", "log.cancel": "Cancellation requested by user",
		"dialog.not_ready.title": "Not ready", "dialog.not_ready.body": "Select an image and a USB device first.",
		"confirm.body":  "All data on this device will be erased\n\n%s %s\n%s\n%.1f GB\nSerial: %s\n\nImage: %s\n%.1f MB\n\nThis action cannot be undone.",
		"confirm.title": "Device data will be erased", "confirm.accept": "Confirm and write", "action.cancel": "Cancel",
		"status.preparing": "Preparing image…", "log.start": "Starting safe write process",
		"stage.writing": "Writing", "stage.verifying": "Verifying",
		"metrics.progress": "Speed: %.1f MiB/s        Processed: %.1f MiB        Remaining: %s",
		"status.failed":    "Failed: %s", "log.error": "Error: %v", "status.cancelled": "Cancelled; device contents may be corrupted",
		"action.retry": "Retry", "status.complete": "Write complete",
		"log.complete":     "Complete: %d bytes, verified=%s, ejected=%s, elapsed=%s",
		"metrics.complete": "Average speed: %.1f MiB/s        Total time: %s", "action.restart": "Start over",
		"card.device": "USB device", "card.image": "Image file", "card.image_info": "Image information",
		"card.options": "Write options", "card.progress": "Status and progress", "log.launched": "GoFlasher started (no telemetry)",
		"filter.images": "USB images", "error.cancelled": "operation cancelled", "bool.true": "yes", "bool.false": "no",
		"picker.image.title": "Choose an image file", "picker.image.accept": "Choose",
	},
	TraditionalChinese: {
		"launcher":     "GoFlasher 圖形介面需使用 'fyne' 建置標籤。安裝 Fyne 建置相依套件後，執行：go run -tags fyne ./cmd/usbwriter",
		"window.title": "GoFlasher USB 寫入工具", "device.none": "未選擇裝置",
		"image.empty":   "映像格式：—\n映像大小：—\nSHA-256：未驗證",
		"option.verify": "寫入後驗證", "option.eject": "完成後安全退出",
		"status.ready": "準備就緒", "metrics.empty": "速度：—        已寫入：—        剩餘：—",
		"log.details": "詳細記錄", "action.start": "開始", "action.copy_error": "複製錯誤資訊",
		"action.copy_log": "複製記錄", "action.rescan": "重新掃描", "action.choose": "選擇",
		"error.devices": "無法讀取裝置：%v", "log.devices": "找到 %d 個允許的 USB 裝置",
		"device.details": "%s %s\n%.1f GB · %s\n序號：%s · USB · 讀卡器：%s · 已掛載：%s · %d 個分割區",
		"image.details":  "映像格式：%s\n壓縮：%s\n檔案大小：%.1f MB\nSHA-256：檢查時產生",
		"log.image":      "已選擇映像 %s", "status.cancelling": "正在取消…", "log.cancel": "使用者要求取消",
		"dialog.not_ready.title": "尚未準備", "dialog.not_ready.body": "請先選擇映像與 USB 裝置。",
		"confirm.body":  "即將清除以下裝置的所有資料\n\n%s %s\n%s\n%.1f GB\n序號：%s\n\n映像：%s\n%.1f MB\n\n此操作無法復原。",
		"confirm.title": "即將清除裝置資料", "confirm.accept": "確認並寫入", "action.cancel": "取消",
		"status.preparing": "正在準備映像…", "log.start": "開始安全寫入流程",
		"stage.writing": "正在寫入", "stage.verifying": "正在驗證",
		"metrics.progress": "速度：%.1f MiB/s        已處理：%.1f MiB        剩餘：%s",
		"status.failed":    "失敗：%s", "log.error": "錯誤：%v", "status.cancelled": "已取消；裝置內容可能已損毀",
		"action.retry": "重試", "status.complete": "寫入完成",
		"log.complete":     "完成：%d bytes，驗證=%s，退出=%s，耗時=%s",
		"metrics.complete": "平均速度：%.1f MiB/s        總耗時：%s", "action.restart": "重新開始",
		"card.device": "USB 裝置", "card.image": "映像檔案", "card.image_info": "映像資訊",
		"card.options": "寫入選項", "card.progress": "狀態與進度", "log.launched": "GoFlasher 啟動（無遙測）",
		"filter.images": "USB 映像", "error.cancelled": "操作已取消", "bool.true": "是", "bool.false": "否",
		"picker.image.title": "選擇映像檔案", "picker.image.accept": "選擇",
	},
}
