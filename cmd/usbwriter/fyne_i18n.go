//go:build fyne

package main

import (
	"encoding/json"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/lang"
)

var fyneTraditionalChinese = map[string]any{
	"Cancel":            "取消",
	"Create Folder":     "建立資料夾",
	"Enter filename":    "輸入檔名",
	"Error":             "錯誤",
	"Favourites":        "常用位置",
	"File":              "檔案",
	"Folder":            "資料夾",
	"New Folder":        "新增資料夾",
	"Open":              "開啟",
	"OK":                "確定",
	"Show Hidden Files": "顯示隱藏檔案",
	"file.name":         map[string]string{"other": "名稱"},
	"file.parent":       map[string]string{"other": "上層"},
}

var fyneEnglish = map[string]any{
	"Cancel": "Cancel", "Create Folder": "Create Folder", "Enter filename": "Enter filename", "Error": "Error",
	"Favourites": "Favourites", "File": "File", "Folder": "Folder", "New Folder": "New Folder", "Open": "Open",
	"OK": "OK", "Show Hidden Files": "Show Hidden Files", "file.name": map[string]string{"other": "Name"}, "file.parent": map[string]string{"other": "Parent"},
}

var fyneSimplifiedChinese = map[string]any{
	"Cancel": "取消", "Create Folder": "新建文件夹", "Enter filename": "输入文件名", "Error": "错误",
	"Favourites": "收藏夹", "File": "文件", "Folder": "文件夹", "New Folder": "新建文件夹", "Open": "打开",
	"OK": "确定", "Show Hidden Files": "显示隐藏文件", "file.name": map[string]string{"other": "名称"}, "file.parent": map[string]string{"other": "上级"},
}

var fyneJapanese = map[string]any{
	"Cancel": "キャンセル", "Create Folder": "フォルダーを作成", "Enter filename": "ファイル名を入力", "Error": "エラー",
	"Favourites": "お気に入り", "File": "ファイル", "Folder": "フォルダー", "New Folder": "新しいフォルダー", "Open": "開く",
	"OK": "OK", "Show Hidden Files": "隠しファイルを表示", "file.name": map[string]string{"other": "名前"}, "file.parent": map[string]string{"other": "親フォルダー"},
}

// configureFyneTranslations supplies strings for controls owned by Fyne's
// bundled file dialog, not just GoFlasher's outer UI.
func configureFyneTranslations(trLocale string) {
	translations := map[string]map[string]any{
		"en":    fyneEnglish,
		"zh-TW": fyneTraditionalChinese,
		"zh-CN": fyneSimplifiedChinese,
		"ja":    fyneJapanese,
	}[trLocale]
	if translations == nil {
		return
	}
	data, err := json.Marshal(translations)
	if err != nil {
		fyne.LogError("Could not encode Fyne translations", err)
		return
	}
	// Register against the active Fyne locale as well as GoFlasher's locale.
	// This keeps the selected GoFlasher language effective even when it differs
	// from the desktop locale.
	if err := lang.AddTranslationsForLocale(data, lang.SystemLocale()); err != nil {
		fyne.LogError("Could not load Fyne translations", err)
	}
}
