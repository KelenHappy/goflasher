//go:build fyne

package main

import (
	"testing"

	"fyne.io/fyne/v2/lang"
)

func TestFyneTraditionalChineseFileDialogTranslations(t *testing.T) {
	want := map[string]string{
		"File": "檔案", "Folder": "資料夾", "Favourites": "常用位置",
		"New Folder": "新增資料夾", "Create Folder": "建立資料夾",
		"Show Hidden Files": "顯示隱藏檔案", "Open": "開啟", "Cancel": "取消",
	}
	for key, value := range want {
		if got := fyneTraditionalChinese[key]; got != value {
			t.Errorf("%q = %q, want %q", key, got, value)
		}
	}
	contextual := map[string]string{
		"file.name":   "名稱",
		"file.parent": "上層",
	}
	for key, value := range contextual {
		message, ok := fyneTraditionalChinese[key].(map[string]string)
		if !ok || message["other"] != value {
			t.Errorf("%s = %#v, want %s", key, fyneTraditionalChinese[key], value)
		}
	}

	t.Setenv("LC_ALL", "zh_TW.UTF-8")
	t.Setenv("LANGUAGE", "")
	configureFyneTranslations("zh-TW")
	if got := lang.X("file.parent", "Parent"); got != "上層" {
		t.Errorf("localized file.parent = %q, want %q", got, "上層")
	}
}
