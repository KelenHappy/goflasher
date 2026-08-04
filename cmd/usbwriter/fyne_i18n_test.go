//go:build fyne

package main

import "testing"

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
	name, ok := fyneTraditionalChinese["file.name"].(map[string]string)
	if !ok || name["other"] != "名稱" {
		t.Errorf("file.name = %#v, want 名稱", fyneTraditionalChinese["file.name"])
	}
}
