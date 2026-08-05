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
	"Favourites":        "常用位置",
	"File":              "檔案",
	"Folder":            "資料夾",
	"New Folder":        "新增資料夾",
	"Open":              "開啟",
	"Show Hidden Files": "顯示隱藏檔案",
	"file.name":         map[string]string{"other": "名稱"},
	"file.parent":       map[string]string{"other": "上層"},
}

// configureFyneTranslations supplies Traditional Chinese strings for controls
// owned by Fyne's bundled file dialog, not just GoFlasher's outer UI.
func configureFyneTranslations(trLocale string) {
	if trLocale != "zh-TW" {
		return
	}
	data, err := json.Marshal(fyneTraditionalChinese)
	if err != nil {
		fyne.LogError("Could not encode Fyne translations", err)
		return
	}
	// Register against the active Fyne locale as well as GoFlasher's locale.
	// This keeps GOFLASHER_LANG=zh-TW effective even on an English desktop and
	// deliberately uses Traditional Chinese on zh-CN systems, matching the app.
	if err := lang.AddTranslationsForLocale(data, lang.SystemLocale()); err != nil {
		fyne.LogError("Could not load Fyne translations", err)
	}
}
