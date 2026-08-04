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

func TestImageChooserLabelsAreLocalized(t *testing.T) {
	tests := []struct {
		locale                 string
		title, accept, dismiss string
	}{
		{locale: "en", title: "Choose an image file", accept: "Choose", dismiss: "Cancel"},
		{locale: "zh-TW", title: "選擇映像檔案", accept: "選擇", dismiss: "取消"},
	}
	for _, tt := range tests {
		tr := New(tt.locale)
		if got := tr.T("picker.image.title"); got != tt.title {
			t.Errorf("%s title = %q, want %q", tt.locale, got, tt.title)
		}
		if got := tr.T("picker.image.accept"); got != tt.accept {
			t.Errorf("%s accept = %q, want %q", tt.locale, got, tt.accept)
		}
		if got := tr.T("action.cancel"); got != tt.dismiss {
			t.Errorf("%s dismiss = %q, want %q", tt.locale, got, tt.dismiss)
		}
	}
}
