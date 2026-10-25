package applog

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type shown struct {
	messages []string
}

func (s *shown) show(_ time.Time, message string) { s.messages = append(s.messages, message) }

func TestLoggerSplitsDisplayAndHistory(t *testing.T) {
	var screen shown
	history := NewHistory(10, "")
	logger := New(NewDisplay(screen.show), history)

	logger.Info("找到 2 個允許的 USB 裝置", EventKey, "log.devices", "count", 2)
	logger.Debug("device enumerated", "path", "/dev/sdb")

	if got := strings.Join(screen.messages, "|"); got != "找到 2 個允許的 USB 裝置" {
		t.Fatalf("displayed %q; Debug records must stay off screen", got)
	}
	lines := history.Lines()
	if len(lines) != 2 {
		t.Fatalf("history kept %d records, want 2: %q", len(lines), lines)
	}
	for _, want := range []string{"level=INFO", "event=log.devices", "count=2"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("Info record %q lacks %q", lines[0], want)
		}
	}
	if !strings.Contains(lines[1], "level=DEBUG") || !strings.Contains(lines[1], "path=/dev/sdb") {
		t.Errorf("Debug record = %q", lines[1])
	}
}

func TestHistoryKeepsNewestRecords(t *testing.T) {
	history := NewHistory(2, "")
	logger := slog.New(history.handler)
	for _, message := range []string{"one", "two", "three"} {
		logger.Info(message)
	}
	lines := history.Lines()
	want := []string{"msg=two", "msg=three"}
	if len(lines) != len(want) {
		t.Fatalf("history = %q, want the two newest records", lines)
	}
	for i, message := range want {
		if !strings.Contains(lines[i], message) {
			t.Errorf("record %d = %q, want %s", i, lines[i], message)
		}
	}
}

func TestHistoryRedactsSerialAndHome(t *testing.T) {
	// Unix paths keep the expectations free of TextHandler's backslash quoting.
	const home = "/home/alice"
	history := NewHistory(10, home)
	logger := slog.New(history.handler).With(SerialKey, "AA11BB22")
	logger.Error("Error: open /home/alice/disk.iso: denied",
		"image", "/home/alice/Downloads/disk.iso",
		"err", errors.New("read /home/alice/disk.iso: EOF"))

	line := strings.Join(history.Lines(), "\n")
	if strings.Contains(line, "AA11BB22") || strings.Contains(line, home) {
		t.Fatalf("record leaked a serial number or home directory: %s", line)
	}
	for _, want := range []string{"serial=[redacted]", "image=~/Downloads/disk.iso", "read ~/disk.iso: EOF", "open ~/disk.iso"} {
		if !strings.Contains(line, want) {
			t.Errorf("record %q lacks %q", line, want)
		}
	}
}

func TestRedactIgnoresCaseAndSlashDirection(t *testing.T) {
	history := NewHistory(10, `C:\Users\BOT`)
	got := history.redactor.text(`open c:\users\bot\a.iso; copy C:/Users/BOT/b.iso`)
	if want := `open ~\a.iso; copy ~/b.iso`; got != want {
		t.Fatalf("redactor.text() = %q, want %q", got, want)
	}
}

func TestRedactDirKeepsOnlyFileNames(t *testing.T) {
	history := NewHistory(10, "/home/alice")
	history.RedactDir("/home/alice/Projects/secret")
	history.RedactDir("/srv/images")
	history.RedactDir("/") // a volume root hides nothing personal
	got := history.redactor.text("/srv/images/x.iso /home/alice/Projects/secret/y.img /home/alice/z.iso /dev/sdb")
	if want := "…/x.iso …/y.img ~/z.iso /dev/sdb"; got != want {
		t.Fatalf("redactor.text() = %q, want %q", got, want)
	}
}

func TestHistoryRedactsDirInFormattedValues(t *testing.T) {
	history := NewHistory(10, "")
	history.RedactDir("/srv/images")
	logger := slog.New(history.handler)
	logger.Error("failed", "err", errors.New("open /srv/images/x.iso: denied"), "paths", []string{"/srv/images/y.iso"})
	line := strings.Join(history.Lines(), "\n")
	if strings.Contains(line, "/srv/images") {
		t.Fatalf("record leaked a redacted directory: %s", line)
	}
}

func TestDisplayIgnoresAttributes(t *testing.T) {
	var screen shown
	logger := slog.New(NewDisplay(screen.show)).With(SerialKey, "AA11BB22").WithGroup("device")
	logger.Warn("Skipped 1 device(s)", "path", "/dev/sdc")
	if got := strings.Join(screen.messages, "|"); got != "Skipped 1 device(s)" {
		t.Fatalf("displayed %q", got)
	}
}
