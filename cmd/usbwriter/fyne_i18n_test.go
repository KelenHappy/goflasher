//go:build fyne

package main

import (
	"reflect"
	"testing"

	"fyne.io/fyne/v2/lang"
	"github.com/goflasher/goflasher/internal/progress"
)

func TestFyneTraditionalChineseTranslations(t *testing.T) {
	assertFyneTraditionalChineseCatalog(t)

	t.Setenv("LC_ALL", "zh_TW.UTF-8")
	t.Setenv("LANGUAGE", "")
	configureFyneTranslations("zh-TW")
	assertTranslation(t, "file.parent", lang.X("file.parent", "Parent"), "上層")
	assertTranslation(t, "Error", lang.L("Error"), "錯誤")
	assertTranslation(t, "OK", lang.L("OK"), "確定")
}

func assertFyneTraditionalChineseCatalog(t *testing.T) {
	t.Helper()
	want := map[string]any{
		"File": "檔案", "Folder": "資料夾", "Favourites": "常用位置",
		"New Folder": "新增資料夾", "Create Folder": "建立資料夾",
		"Show Hidden Files": "顯示隱藏檔案", "Open": "開啟", "Cancel": "取消",
		"Error": "錯誤", "OK": "確定",
		"file.name":   map[string]string{"other": "名稱"},
		"file.parent": map[string]string{"other": "上層"},
	}
	for key, value := range want {
		assertCatalogEntry(t, key, fyneTraditionalChinese[key], value)
	}
}

func assertCatalogEntry(t *testing.T, key string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%q = %#v, want %#v", key, got, want)
	}
}

func assertTranslation(t *testing.T, key, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("localized %s = %q, want %q", key, got, want)
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
