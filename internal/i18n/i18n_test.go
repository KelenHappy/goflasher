package i18n

import "testing"

func TestParseLocale(t *testing.T) {
	tests := map[string]Locale{"zh_TW.UTF-8": TraditionalChinese, "zh-CN": TraditionalChinese, "en_US.UTF-8": English, "C": English, "": English}
	for input, want := range tests {
		if got := ParseLocale(input); got != want {
			t.Errorf("ParseLocale(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCatalogsHaveSameMessages(t *testing.T) {
	for id := range catalogs[English] {
		if _, ok := catalogs[TraditionalChinese][id]; !ok {
			t.Errorf("Traditional Chinese catalog is missing %q", id)
		}
	}
	for id := range catalogs[TraditionalChinese] {
		if _, ok := catalogs[English][id]; !ok {
			t.Errorf("English catalog is missing %q", id)
		}
	}
}

func TestTranslateAndFallback(t *testing.T) {
	if got := New("zh-TW").T("log.devices", 2); got != "找到 2 個允許的 USB 裝置" {
		t.Fatalf("unexpected translation: %q", got)
	}
	if got := New("unsupported").T("status.ready"); got != "Ready" {
		t.Fatalf("unexpected fallback: %q", got)
	}
	if got := New("en").T("missing.message"); got != "missing.message" {
		t.Fatalf("unexpected missing-message result: %q", got)
	}
}
