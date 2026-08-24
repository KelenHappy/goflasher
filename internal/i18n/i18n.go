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
	SimplifiedChinese  Locale = "zh-CN"
	Japanese           Locale = "ja"
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
	language = localeBase(language)
	if locale, ok := localeAliases[language]; ok {
		return locale
	}
	for _, match := range localePrefixes {
		if strings.HasPrefix(language, match.prefix) {
			return match.locale
		}
	}
	return English
}

func localeBase(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	if i := strings.IndexAny(language, ".@"); i >= 0 {
		language = language[:i]
	}
	return strings.ReplaceAll(language, "_", "-")
}

var localeAliases = map[string]Locale{
	"ja":    Japanese,
	"jp":    Japanese,
	"zh":    TraditionalChinese,
	"zh-cn": SimplifiedChinese,
	"zh-sg": SimplifiedChinese,
}

var localePrefixes = []struct {
	prefix string
	locale Locale
}{
	{prefix: "zh-hans", locale: SimplifiedChinese},
	{prefix: "zh-", locale: TraditionalChinese},
	{prefix: "ja-", locale: Japanese},
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
		"error.devices": "Cannot read devices: %v", "error.operation": "Operation failed", "error.iso_unsupported": "Unsupported or ambiguous ISO", "error.uefi_loader": "Required UEFI x64 loader is missing", "error.target_small": "Target USB device is too small", "error.temporary_space": "Insufficient temporary disk space", "error.libwim_unavailable": "Bundled libwim is unavailable", "error.libwim_abi": "Bundled libwim ABI or version does not match", "error.wim_split": "WIM splitting failed", "error.gpt_verify": "GPT layout verification failed", "error.fat_verify": "FAT32 filesystem verification failed", "error.device_changed": "Target device identity changed", "log.devices": "Found %d allowed USB devices",
		"device.details": "%s %s\n%.1f GB · %s\nSerial: %s · USB · Card reader: %s · Mounted: %s · %d partitions",
		"image.details":  "Image format: %s\nCompression: %s\nFile size: %.1f MB\nSHA-256: generated during verification",
		"log.image":      "Selected image %s", "status.cancelling": "Cancelling…", "log.cancel": "Cancellation requested by user",
		"dialog.not_ready.title": "Not ready", "dialog.not_ready.body": "Select an image and a USB device first.",
		"confirm.body":  "All data on this device will be erased\n\n%s %s\n%s\n%.1f GB\nSerial: %s\n\nImage: %s\n%.1f MB\n\nThis action cannot be undone.",
		"confirm.title": "Device data will be erased", "confirm.accept": "Confirm and write", "action.cancel": "Cancel",
		"status.preparing": "Preparing image…", "log.start": "Starting safe write process",
		"plan.windows.summary": "Windows installer plan\nPartition table: %s\nFilesystem: %s\nBoot mode: %s\ninstall.wim split required: %s\nReason: %s\nRequired USB capacity: %.2f GiB\nRequired temporary space: %.2f GiB\nAvailable temporary space: %.2f GiB\n\nWarning: the entire target USB device will be erased.", "plan.split.reason.none": "not required", "plan.split.reason.fat32": "install.wim exceeds the FAT32 single-file size limit",
		"stage.inspecting": "Analyzing ISO", "stage.planning": "Planning GPT layout", "stage.staging_wim": "Staging WIM", "stage.splitting_wim": "Splitting WIM", "stage.partitioning": "Partitioning", "stage.formatting": "Creating FAT32 filesystem", "stage.extracting": "Extracting files", "stage.verifying_filesystem": "Verifying filesystem", "stage.writing": "Writing", "stage.decompress_writing": "Decompressing and writing", "stage.flushing": "Synchronizing device", "stage.verifying": "Verifying", "stage.ejecting": "Safely ejecting",
		"metrics.progress": "Average speed: %.1f MiB/s        Processed: %.1f MiB        Remaining: %s", "metrics.formatting": "Progress: %d%%", "metrics.finalizing": "Speed: —        Finalizing device…",
		"status.failed": "Failed: %s", "log.error": "Error: %v", "status.cancelled": "Cancelled; device contents may be corrupted",
		"action.retry": "Retry", "status.complete": "Write complete",
		"log.complete":           "Complete: %d bytes, verified=%s, ejected=%s, elapsed=%s",
		"log.installer.complete": "Installer complete: %d files, %d WIM parts, semantic verification=%s, ejected=%s, elapsed=%s",
		"metrics.complete":       "Average speed: %.1f MiB/s        Total time: %s", "action.restart": "Start over",
		"card.device": "USB device", "card.image": "Image file", "card.image_info": "Image information",
		"card.options": "Write options", "card.progress": "Status and progress", "log.launched": "GoFlasher started (no telemetry)",
		"filter.images": "USB images", "error.cancelled": "operation cancelled", "bool.true": "yes", "bool.false": "no",
		"picker.image.title": "Choose an image file", "picker.image.accept": "Choose",
		"action.format_fat32": "Format FAT32", "dialog.format.not_ready": "Select a USB device first.",
		"format.confirm.title": "Format USB device as FAT32", "format.confirm.accept": "Erase and format",
		"format.confirm.body": "All data and partitions on this device will be erased\n\n%s %s\n%s\n%.1f GB\nSerial: %s\n\nA FAT32 filesystem named GOFLASHER will be created. This action cannot be undone.",
		"status.formatting":   "Formatting as FAT32…", "status.format.complete": "FAT32 format complete",
		"log.format.start": "Formatting %s as FAT32", "log.format.complete": "FAT32 format complete",
		"action.settings": "Settings", "settings.title": "Settings", "settings.language": "Language", "settings.close": "Close",
		"settings.theme": "Theme", "settings.theme.system": "System", "settings.theme.light": "Light", "settings.theme.dark": "Dark",
	},
	TraditionalChinese: {
		"launcher":     "GoFlasher 圖形介面需使用 'fyne' 建置標籤。安裝 Fyne 建置相依套件後，執行：go run -tags fyne ./cmd/usbwriter",
		"window.title": "GoFlasher USB 寫入工具", "device.none": "未選擇裝置",
		"image.empty":   "映像格式：—\n映像大小：—\nSHA-256：未驗證",
		"option.verify": "寫入後驗證", "option.eject": "完成後安全退出",
		"status.ready": "準備就緒", "metrics.empty": "速度：—        已寫入：—        剩餘：—",
		"log.details": "詳細記錄", "action.start": "開始", "action.copy_error": "複製錯誤資訊",
		"action.copy_log": "複製記錄", "action.rescan": "重新掃描", "action.choose": "選擇",
		"error.devices": "無法讀取裝置：%v", "error.operation": "操作失敗", "error.iso_unsupported": "不支援或無法明確分類的ISO", "error.uefi_loader": "缺少必要的UEFI x64 loader", "error.target_small": "目標USB裝置容量不足", "error.temporary_space": "暫存磁碟空間不足", "error.libwim_unavailable": "Bundled libwim無法使用", "error.libwim_abi": "Bundled libwim ABI或版本不符", "error.wim_split": "WIM分割失敗", "error.gpt_verify": "GPT配置驗證失敗", "error.fat_verify": "FAT32檔案系統驗證失敗", "error.device_changed": "目標裝置identity已變更", "log.devices": "找到 %d 個允許的 USB 裝置",
		"device.details": "%s %s\n%.1f GB · %s\n序號：%s · USB · 讀卡器：%s · 已掛載：%s · %d 個分割區",
		"image.details":  "映像格式：%s\n壓縮：%s\n檔案大小：%.1f MB\nSHA-256：檢查時產生",
		"log.image":      "已選擇映像 %s", "status.cancelling": "正在取消…", "log.cancel": "使用者要求取消",
		"dialog.not_ready.title": "尚未準備", "dialog.not_ready.body": "請先選擇映像與 USB 裝置。",
		"confirm.body":  "即將清除以下裝置的所有資料\n\n%s %s\n%s\n%.1f GB\n序號：%s\n\n映像：%s\n%.1f MB\n\n此操作無法復原。",
		"confirm.title": "即將清除裝置資料", "confirm.accept": "確認並寫入", "action.cancel": "取消",
		"status.preparing": "正在準備映像…", "log.start": "開始安全寫入流程",
		"plan.windows.summary": "Windows安裝媒體計畫\n分割表：%s\n檔案系統：%s\n開機模式：%s\n需要分割install.wim：%s\n原因：%s\nUSB所需容量：%.2f GiB\n所需暫存空間：%.2f GiB\n可用暫存空間：%.2f GiB\n\n警告：將清除整個目標USB裝置。", "plan.split.reason.none": "不需要", "plan.split.reason.fat32": "install.wim超過FAT32單一檔案大小上限",
		"stage.inspecting": "正在分析ISO", "stage.planning": "正在規劃GPT配置", "stage.staging_wim": "正在暫存 WIM", "stage.splitting_wim": "正在分割 WIM", "stage.partitioning": "正在建立分割區", "stage.formatting": "正在建立FAT32檔案系統", "stage.extracting": "正在擷取檔案", "stage.verifying_filesystem": "正在驗證檔案系統", "stage.writing": "正在寫入", "stage.decompress_writing": "正在解壓縮並寫入", "stage.flushing": "正在同步裝置", "stage.verifying": "正在驗證", "stage.ejecting": "正在安全退出",
		"metrics.progress": "平均速度：%.1f MiB/s        已處理：%.1f MiB        剩餘：%s", "metrics.formatting": "進度：%d%%", "metrics.finalizing": "速度：—        正在完成裝置作業…",
		"status.failed": "失敗：%s", "log.error": "錯誤：%v", "status.cancelled": "已取消；裝置內容可能已損毀",
		"action.retry": "重試", "status.complete": "寫入完成",
		"log.complete":           "完成：%d bytes，驗證=%s，退出=%s，耗時=%s",
		"log.installer.complete": "安裝媒體完成：%d 個檔案，%d 個 WIM 分片，語意驗證=%s，退出=%s，耗時=%s",
		"metrics.complete":       "平均速度：%.1f MiB/s        總耗時：%s", "action.restart": "重新開始",
		"card.device": "USB 裝置", "card.image": "映像檔案", "card.image_info": "映像資訊",
		"card.options": "寫入選項", "card.progress": "狀態與進度", "log.launched": "GoFlasher 啟動（無遙測）",
		"filter.images": "USB 映像", "error.cancelled": "操作已取消", "bool.true": "是", "bool.false": "否",
		"picker.image.title": "選擇映像檔案", "picker.image.accept": "選擇",
		"action.format_fat32": "格式化 FAT32", "dialog.format.not_ready": "請先選擇 USB 裝置。",
		"format.confirm.title": "將 USB 裝置格式化為 FAT32", "format.confirm.accept": "清除並格式化",
		"format.confirm.body": "即將清除以下裝置的所有資料與分割區\n\n%s %s\n%s\n%.1f GB\n序號：%s\n\n將建立名為 GOFLASHER 的 FAT32 檔案系統。此操作無法復原。",
		"status.formatting":   "正在格式化為 FAT32…", "status.format.complete": "FAT32 格式化完成",
		"log.format.start": "正在將 %s 格式化為 FAT32", "log.format.complete": "FAT32 格式化完成",
		"action.settings": "設定", "settings.title": "設定", "settings.language": "語言", "settings.close": "關閉",
		"settings.theme": "主題", "settings.theme.system": "跟隨系統", "settings.theme.light": "淺色", "settings.theme.dark": "深色",
	},
	SimplifiedChinese: {
		"launcher":     "GoFlasher 图形界面需要使用 'fyne' 构建标签。安装 Fyne 构建依赖后运行：go run -tags fyne ./cmd/usbwriter",
		"window.title": "GoFlasher USB 写入工具", "device.none": "未选择设备",
		"image.empty": "镜像格式：—\n镜像大小：—\nSHA-256：未验证", "option.verify": "写入后验证", "option.eject": "完成后安全弹出",
		"status.ready": "准备就绪", "metrics.empty": "速度：—        已写入：—        剩余：—", "log.details": "详细日志",
		"action.start": "开始", "action.copy_error": "复制错误详情", "action.copy_log": "复制日志", "action.rescan": "重新扫描", "action.choose": "选择",
		"error.devices": "无法读取设备：%v", "error.operation": "操作失败", "error.iso_unsupported": "不支持或无法明确分类的ISO", "error.uefi_loader": "缺少所需UEFI x64加载器", "error.target_small": "目标USB设备容量不足", "error.temporary_space": "临时磁盘空间不足", "error.libwim_unavailable": "Bundled libwim不可用", "error.libwim_abi": "Bundled libwim ABI或版本不匹配", "error.wim_split": "WIM拆分失败", "error.gpt_verify": "GPT布局验证失败", "error.fat_verify": "FAT32文件系统验证失败", "error.device_changed": "目标设备标识已更改", "log.devices": "找到 %d 个允许的 USB 设备", "device.details": "%s %s\n%.1f GB · %s\n序列号：%s · USB · 读卡器：%s · 已挂载：%s · %d 个分区",
		"image.details": "镜像格式：%s\n压缩：%s\n文件大小：%.1f MB\nSHA-256：验证时生成", "log.image": "已选择镜像 %s", "status.cancelling": "正在取消…", "log.cancel": "用户请求取消",
		"dialog.not_ready.title": "尚未准备好", "dialog.not_ready.body": "请先选择镜像和 USB 设备。", "confirm.body": "将清除此设备上的所有数据\n\n%s %s\n%s\n%.1f GB\n序列号：%s\n\n镜像：%s\n%.1f MB\n\n此操作无法撤销。",
		"confirm.title": "设备数据将被清除", "confirm.accept": "确认并写入", "action.cancel": "取消", "status.preparing": "正在准备镜像…", "log.start": "开始安全写入流程",
		"plan.windows.summary": "Windows安装介质计划\n分区表：%s\n文件系统：%s\n启动模式：%s\n需要拆分install.wim：%s\n原因：%s\n所需USB容量：%.2f GiB\n所需临时空间：%.2f GiB\n可用临时空间：%.2f GiB\n\n警告：将擦除整个目标USB设备。", "plan.split.reason.none": "不需要", "plan.split.reason.fat32": "install.wim超过FAT32单文件大小限制",
		"stage.inspecting": "正在檢查", "stage.planning": "正在規劃", "stage.staging_wim": "正在暫存 WIM", "stage.splitting_wim": "正在分割 WIM", "stage.partitioning": "正在建立分割區", "stage.formatting": "正在格式化", "stage.extracting": "正在擷取檔案", "stage.verifying_filesystem": "正在驗證檔案系統", "stage.writing": "正在写入", "stage.decompress_writing": "正在解压并写入", "stage.flushing": "正在同步设备", "stage.verifying": "正在验证", "stage.ejecting": "正在安全弹出",
		"metrics.progress": "平均速度：%.1f MiB/s        已处理：%.1f MiB        剩余：%s", "metrics.formatting": "进度：%d%%", "metrics.finalizing": "速度：—        正在完成设备操作…",
		"status.failed": "失败：%s", "log.error": "错误：%v", "status.cancelled": "已取消；设备内容可能已损坏", "action.retry": "重试", "status.complete": "写入完成",
		"log.complete": "完成：%d 字节，验证=%s，弹出=%s，耗时=%s", "log.installer.complete": "安装介质完成：%d 个文件，%d 个 WIM 分片，语义验证=%s，弹出=%s，耗时=%s", "metrics.complete": "平均速度：%.1f MiB/s        总耗时：%s", "action.restart": "重新开始",
		"card.device": "USB 设备", "card.image": "镜像文件", "card.image_info": "镜像信息", "card.options": "写入选项", "card.progress": "状态和进度", "log.launched": "GoFlasher 已启动（无遥测）",
		"filter.images": "USB 镜像", "error.cancelled": "操作已取消", "bool.true": "是", "bool.false": "否", "picker.image.title": "选择镜像文件", "picker.image.accept": "选择",
		"action.format_fat32": "格式化 FAT32", "dialog.format.not_ready": "请先选择 USB 设备。", "format.confirm.title": "将 USB 设备格式化为 FAT32", "format.confirm.accept": "清除并格式化",
		"format.confirm.body": "将清除此设备上的所有数据和分区\n\n%s %s\n%s\n%.1f GB\n序列号：%s\n\n将创建名为 GOFLASHER 的 FAT32 文件系统。此操作无法撤销。",
		"status.formatting":   "正在格式化为 FAT32…", "status.format.complete": "FAT32 格式化完成", "log.format.start": "正在将 %s 格式化为 FAT32", "log.format.complete": "FAT32 格式化完成",
		"action.settings": "设置", "settings.title": "设置", "settings.language": "语言", "settings.close": "关闭",
		"settings.theme": "主题", "settings.theme.system": "跟随系统", "settings.theme.light": "浅色", "settings.theme.dark": "深色",
	},
	Japanese: {
		"launcher":     "GoFlasher GUI には 'fyne' ビルドタグが必要です。Fyne の依存関係をインストールして実行してください：go run -tags fyne ./cmd/usbwriter",
		"window.title": "GoFlasher USB ライター", "device.none": "デバイスが選択されていません", "image.empty": "イメージ形式：—\nイメージサイズ：—\nSHA-256：未検証",
		"option.verify": "書き込み後に検証", "option.eject": "完了後に安全に取り出す", "status.ready": "準備完了", "metrics.empty": "速度：—        書き込み済み：—        残り：—", "log.details": "詳細ログ",
		"action.start": "開始", "action.copy_error": "エラー詳細をコピー", "action.copy_log": "ログをコピー", "action.rescan": "再スキャン", "action.choose": "選択",
		"error.devices": "デバイスを読み取れません：%v", "error.operation": "操作に失敗しました", "error.iso_unsupported": "未対応または判別不能なISO", "error.uefi_loader": "必要なUEFI x64ローダーがありません", "error.target_small": "対象USBデバイスの容量が不足しています", "error.temporary_space": "一時ディスク容量が不足しています", "error.libwim_unavailable": "Bundled libwimを利用できません", "error.libwim_abi": "Bundled libwimのABIまたはバージョンが一致しません", "error.wim_split": "WIM分割に失敗しました", "error.gpt_verify": "GPTレイアウト検証に失敗しました", "error.fat_verify": "FAT32ファイルシステム検証に失敗しました", "error.device_changed": "対象デバイスの識別情報が変更されました", "log.devices": "許可された USB デバイスが %d 台見つかりました", "device.details": "%s %s\n%.1f GB · %s\nシリアル：%s · USB · カードリーダー：%s · マウント済み：%s · %d パーティション",
		"image.details": "イメージ形式：%s\n圧縮：%s\nファイルサイズ：%.1f MB\nSHA-256：検証時に生成", "log.image": "イメージ %s を選択しました", "status.cancelling": "キャンセル中…", "log.cancel": "ユーザーがキャンセルを要求しました",
		"dialog.not_ready.title": "準備未完了", "dialog.not_ready.body": "先にイメージと USB デバイスを選択してください。", "confirm.body": "このデバイスのすべてのデータが消去されます\n\n%s %s\n%s\n%.1f GB\nシリアル：%s\n\nイメージ：%s\n%.1f MB\n\nこの操作は元に戻せません。",
		"confirm.title": "デバイスデータが消去されます", "confirm.accept": "確認して書き込む", "action.cancel": "キャンセル", "status.preparing": "イメージを準備中…", "log.start": "安全な書き込み処理を開始します",
		"plan.windows.summary": "Windowsインストーラープラン\nパーティションテーブル：%s\nファイルシステム：%s\nブートモード：%s\ninstall.wim分割が必要：%s\n理由：%s\n必要USB容量：%.2f GiB\n必要一時領域：%.2f GiB\n利用可能一時領域：%.2f GiB\n\n警告：対象USBデバイス全体が消去されます。", "plan.split.reason.none": "不要", "plan.split.reason.fat32": "install.wimがFAT32の単一ファイル上限を超えています",
		"stage.inspecting": "検査中", "stage.planning": "計画中", "stage.staging_wim": "WIMを準備中", "stage.splitting_wim": "WIMを分割中", "stage.partitioning": "パーティション作成中", "stage.formatting": "フォーマット中", "stage.extracting": "ファイル抽出中", "stage.verifying_filesystem": "ファイルシステム検証中", "stage.writing": "書き込み中", "stage.decompress_writing": "展開して書き込み中", "stage.flushing": "デバイスを同期中", "stage.verifying": "検証中", "stage.ejecting": "安全に取り出し中",
		"metrics.progress": "平均速度：%.1f MiB/s        処理済み：%.1f MiB        残り：%s", "metrics.formatting": "進捗：%d%%", "metrics.finalizing": "速度：—        デバイスを仕上げています…",
		"status.failed": "失敗：%s", "log.error": "エラー：%v", "status.cancelled": "キャンセルしました。デバイスの内容が破損している可能性があります", "action.retry": "再試行", "status.complete": "書き込み完了",
		"log.complete": "完了：%d バイト、検証=%s、取り出し=%s、経過時間=%s", "log.installer.complete": "インストーラー完了：%d ファイル、%d WIM パート、意味検証=%s、取り出し=%s、経過=%s", "metrics.complete": "平均速度：%.1f MiB/s        合計時間：%s", "action.restart": "最初からやり直す",
		"card.device": "USB デバイス", "card.image": "イメージファイル", "card.image_info": "イメージ情報", "card.options": "書き込みオプション", "card.progress": "状態と進捗", "log.launched": "GoFlasher を起動しました（テレメトリなし）",
		"filter.images": "USB イメージ", "error.cancelled": "操作がキャンセルされました", "bool.true": "はい", "bool.false": "いいえ", "picker.image.title": "イメージファイルを選択", "picker.image.accept": "選択",
		"action.format_fat32": "FAT32 でフォーマット", "dialog.format.not_ready": "先に USB デバイスを選択してください。", "format.confirm.title": "USB デバイスを FAT32 でフォーマット", "format.confirm.accept": "消去してフォーマット",
		"format.confirm.body": "このデバイスのすべてのデータとパーティションが消去されます\n\n%s %s\n%s\n%.1f GB\nシリアル：%s\n\nGOFLASHER という名前の FAT32 ファイルシステムを作成します。この操作は元に戻せません。",
		"status.formatting":   "FAT32 でフォーマット中…", "status.format.complete": "FAT32 フォーマット完了", "log.format.start": "%s を FAT32 でフォーマット中", "log.format.complete": "FAT32 フォーマット完了",
		"action.settings": "設定", "settings.title": "設定", "settings.language": "言語", "settings.close": "閉じる",
		"settings.theme": "テーマ", "settings.theme.system": "システム", "settings.theme.light": "ライト", "settings.theme.dark": "ダーク",
	},
}
