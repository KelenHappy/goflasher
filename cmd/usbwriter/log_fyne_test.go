//go:build fyne && (linux || windows || darwin)

package main

import (
	"log/slog"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/i18n"
)

func TestLogEventShowsLocalizedMessageAndKeepsEventID(t *testing.T) {
	c := newTestController(t)
	onGUI(func() { c.setLanguage(i18n.TraditionalChinese) })
	onGUI(func() {
		c.logEvent(slog.LevelInfo, "log.devices", 2)
		c.logger.Debug("device available", deviceAttrs(device.Device{Path: "/dev/sdb", Serial: "AA11BB22"})...)
	})
	waitFor(t, func() bool { return strings.Contains(c.view.logs.Text, "找到 2 個允許的 USB 裝置") })
	if strings.Contains(c.view.logs.Text, "device available") {
		t.Fatalf("Debug record reached the screen: %q", c.view.logs.Text)
	}
	history := strings.Join(c.history.Lines(), "\n")
	for _, want := range []string{"level=INFO", "event=log.devices", "level=DEBUG", "path=/dev/sdb"} {
		if !strings.Contains(history, want) {
			t.Errorf("history lacks %q:\n%s", want, history)
		}
	}
	if strings.Contains(history, "AA11BB22") {
		t.Fatalf("history leaked a serial number:\n%s", history)
	}
}

func TestImageFolderIsRedactedFromHistory(t *testing.T) {
	c := newTestController(t)
	onGUI(func() { c.detectImage("/srv/private-folder/missing.iso") })
	waitFor(t, func() bool { return strings.Contains(c.view.logs.Text, "private-folder") })
	history := strings.Join(c.history.Lines(), "\n")
	if strings.Contains(history, "private-folder") {
		t.Errorf("history leaked the image folder:\n%s", history)
	}
	if !strings.Contains(history, "missing.iso") {
		t.Errorf("history lost the image file name:\n%s", history)
	}
}

func TestLogFileNameSortsByTime(t *testing.T) {
	now := time.Date(2026, 10, 25, 9, 4, 5, 0, time.UTC)
	if got, want := logFileName(now), "goflasher-20261025-090405.log"; got != want {
		t.Fatalf("logFileName() = %q, want %q", got, want)
	}
}

func TestLogFileContentsAddsBuildAndDateHeader(t *testing.T) {
	now := time.Date(2026, 10, 25, 9, 4, 5, 0, time.UTC)
	got := string(logFileContents("v1.2.3 (go1.26)", []string{"09:00:00 started", "09:01:00 done"}, now))
	want := "# GoFlasher v1.2.3 (go1.26), log saved 2026-10-25T09:04:05Z (" + runtime.GOOS + "/" + runtime.GOARCH + ")\n" +
		"09:00:00 started\n09:01:00 done\n"
	if got != want {
		t.Fatalf("logFileContents() = %q, want %q", got, want)
	}
}

func TestBuildDescription(t *testing.T) {
	tests := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{name: "unavailable", want: "unknown build"},
		{
			name: "release",
			info: &debug.BuildInfo{GoVersion: "go1.26.10", Main: debug.Module{Version: "v0.1.0-alphav9"}, Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "097d2312cd16bf23ede1b7afa2584f524c0bc161"},
				{Key: "vcs.time", Value: "2026-10-23T08:00:00Z"},
				{Key: "vcs.modified", Value: "false"},
			}},
			ok:   true,
			want: "v0.1.0-alphav9 (097d2312cd16, 2026-10-23T08:00:00Z, go1.26.10)",
		},
		{
			name: "local modified build",
			info: &debug.BuildInfo{GoVersion: "go1.26.10", Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "abc"},
				{Key: "vcs.modified", Value: "true"},
			}},
			ok:   true,
			want: "(devel) (abc, modified, go1.26.10)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildDescription(tt.info, tt.ok); got != tt.want {
				t.Fatalf("buildDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSaveLogButtonFollowsLanguage(t *testing.T) {
	c := newTestController(t)
	if got := c.view.saveLog.Text; got != "Save log" {
		t.Fatalf("English label = %q", got)
	}
	onGUI(func() { c.setLanguage(i18n.TraditionalChinese) })
	if got := c.view.saveLog.Text; got != "儲存記錄" {
		t.Fatalf("Traditional Chinese label = %q", got)
	}
}
