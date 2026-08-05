//go:build fyne

package main

import (
	"testing"

	"fyne.io/fyne/v2/lang"
	"github.com/goflasher/goflasher/internal/progress"
)

func TestFyneTraditionalChineseTranslations(t *testing.T) {
	want := map[string]string{
		"File": "檔案", "Folder": "資料夾", "Favourites": "常用位置",
		"New Folder": "新增資料夾", "Create Folder": "建立資料夾",
		"Show Hidden Files": "顯示隱藏檔案", "Open": "開啟", "Cancel": "取消",
		"Error": "錯誤", "OK": "確定",
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
	if got := lang.L("Error"); got != "錯誤" {
		t.Errorf("localized Error = %q, want %q", got, "錯誤")
	}
	if got := lang.L("OK"); got != "確定" {
		t.Errorf("localized OK = %q, want %q", got, "確定")
	}
}

func TestOverallProgressDoesNotCompleteBeforeWorkflow(t *testing.T) {
	fullWrite := progress.Update{Stage: progress.StageWriting, BytesProcessed: 100, TotalBytes: 100}
	if got := overallProgress(fullWrite, false); got >= 1 {
		t.Fatalf("write-only progress completed early: %v", got)
	}
	if got := overallProgress(fullWrite, true); got != 0.45 {
		t.Fatalf("write progress with verification = %v", got)
	}
	fullVerify := progress.Update{Stage: progress.StageVerifying, BytesProcessed: 100, TotalBytes: 100}
	if got := overallProgress(fullVerify, true); got >= 1 {
		t.Fatalf("verification progress completed before finalization: %v", got)
	}
}
