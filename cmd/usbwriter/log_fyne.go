//go:build fyne && (linux || windows || darwin)

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"github.com/goflasher/goflasher/internal/applog"
	"github.com/goflasher/goflasher/internal/device"
)

const (
	// maxLogLines bounds the on-screen log.
	maxLogLines = 500
	// historyLimit bounds the exportable history, which also holds Debug
	// records that never reach the screen.
	historyLimit = 2000
)

// initLogging routes c.logger to the on-screen log and the exportable history.
// Every GUI log record goes through c.logger.
func (c *guiController) initLogging() {
	home, _ := os.UserHomeDir() // without a home directory there is nothing to redact
	c.history = applog.NewHistory(historyLimit, home)
	c.logger = applog.New(applog.NewDisplay(c.appendLog), c.history)
}

// logEvent records the localized catalog message id for the screen, tagged
// with id itself for the exported history.
func (c *guiController) logEvent(level slog.Level, id string, args ...any) {
	c.logger.Log(context.Background(), level, c.tr.T(id, args...), applog.EventKey, id)
}

// appendLog shows one record in the on-screen log. slog handlers may run on
// any goroutine, so the widget update is queued for the Fyne thread.
func (c *guiController) appendLog(at time.Time, message string) {
	line := at.Format("15:04:05 ") + message
	fyne.Do(func() {
		c.logLines = append(c.logLines, line)
		if len(c.logLines) > maxLogLines {
			c.logLines = c.logLines[len(c.logLines)-maxLogLines:]
		}
		c.view.logs.SetText(strings.Join(c.logLines, "\n"))
	})
}

// deviceAttrs describes d for Debug records. The serial number is left out;
// applog would redact it anyway.
func deviceAttrs(d device.Device) []any {
	return []any{
		"path", d.Path, "id", d.ID, "vendor", d.Vendor, "model", d.Model,
		"size", d.Size, "card_reader", d.IsCardReader, "mounted", d.Mounted,
		"partitions", d.PartitionCount,
	}
}

// logSaveLabels holds the localized text shown by the log save chooser.
type logSaveLabels struct {
	Title    string
	Accept   string
	Dismiss  string
	Filter   string
	FileName string
}

// exportLog saves the redacted history only when the user asks for it;
// GoFlasher never writes a log file on its own.
func (c *guiController) exportLog() {
	now := time.Now()
	labels := logSaveLabels{
		Title:    c.tr.T("picker.log.title"),
		Accept:   c.tr.T("picker.log.accept"),
		Dismiss:  c.tr.T("action.cancel"),
		Filter:   c.tr.T("filter.logs"),
		FileName: logFileName(now),
	}
	c.view.saveLog.Disable()
	contents := logFileContents(buildDescription(debug.ReadBuildInfo()), c.history.Lines(), now)
	saveLogFile(c.view.window, labels, contents, func(path string, err error) {
		c.view.saveLog.Enable()
		if err != nil {
			c.logEvent(slog.LevelError, "log.error", err)
			dialog.ShowError(err, c.view.window)
			return
		}
		if path != "" {
			c.history.RedactDir(filepath.Dir(path))
			c.logEvent(slog.LevelInfo, "log.saved", path)
		}
	})
}

func logFileName(now time.Time) string {
	return now.Format("goflasher-20060102-150405.log")
}

// logFileContents prefixes the records with the build, save time, and
// platform, so a report names the release it came from.
func logFileContents(build string, lines []string, now time.Time) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# GoFlasher %s, log saved %s (%s/%s)\n", build, now.Format(time.RFC3339), runtime.GOOS, runtime.GOARCH)
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// buildDescription names the build from the module version and the VCS stamp
// that go build embeds; it needs no linker flags.
func buildDescription(info *debug.BuildInfo, ok bool) string {
	if !ok {
		return "unknown build"
	}
	details := []string{}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if revision := settings["vcs.revision"]; revision != "" {
		details = append(details, revision[:min(len(revision), 12)])
	}
	if at := settings["vcs.time"]; at != "" {
		details = append(details, at)
	}
	if settings["vcs.modified"] == "true" {
		details = append(details, "modified")
	}
	details = append(details, info.GoVersion)
	return fmt.Sprintf("%s (%s)", info.Main.Version, strings.Join(details, ", "))
}
