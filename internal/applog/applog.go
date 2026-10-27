// Package applog provides the GUI's log/slog plumbing. One *slog.Logger fans
// out to a Display, which shows user-facing messages on screen, and a History,
// which keeps every record as redacted text for an explicit export. Nothing is
// written to disk unless the user saves the history.
package applog

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// EventKey carries a record's stable message catalog ID, so an exported log
// stays searchable whatever language the interface used.
const EventKey = "event"

// SerialKey names attributes holding device serial numbers, which Redact
// removes.
const SerialKey = "serial"

const (
	redacted        = "[redacted]"
	homeReplacement = "~"
	dirReplacement  = "…"
)

// New returns a logger that sends Info and above to display and every record
// to history.
func New(display *Display, history *History) *slog.Logger {
	return slog.New(slog.NewMultiHandler(display, history.handler))
}

// Display is an slog.Handler that passes Info-and-above messages to show. It
// ignores attributes: the message is already localized for the user, and the
// attributes are diagnostics kept in the History.
type Display struct {
	show func(time.Time, string)
}

// NewDisplay returns a Display that calls show for each displayed record.
func NewDisplay(show func(time.Time, string)) *Display { return &Display{show: show} }

func (d *Display) Enabled(_ context.Context, level slog.Level) bool { return level >= slog.LevelInfo }

func (d *Display) Handle(_ context.Context, r slog.Record) error {
	d.show(r.Time, r.Message)
	return nil
}

func (d *Display) WithAttrs([]slog.Attr) slog.Handler { return d }
func (d *Display) WithGroup(string) slog.Handler      { return d }

// History keeps the most recent records, at every level, as redacted
// slog text lines.
type History struct {
	handler  slog.Handler
	lines    *lineBuffer
	redactor *redactor
}

// NewHistory keeps at most limit records and replaces homeDir with "~".
// Redaction happens as records are stored, so unredacted diagnostics never
// reach an exported file.
func NewHistory(limit int, homeDir string) *History {
	r := &redactor{}
	r.add(rule{secret: homeDir, replacement: homeReplacement})
	lines := &lineBuffer{limit: limit}
	handler := slog.NewTextHandler(lines, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: r.attr})
	return &History{handler: handler, lines: lines, redactor: r}
}

// Lines returns a copy of the stored records, oldest first.
func (h *History) Lines() []string { return h.lines.snapshot() }

// RedactDir hides dir, such as the folder of a chosen image, in records
// stored from now on, leaving only the file names below it. A volume root is
// ignored: hiding it would mangle every path without hiding anything personal.
func (h *History) RedactDir(dir string) {
	if clean := filepath.Clean(dir); dir == "" || filepath.Dir(clean) == clean {
		return
	}
	h.redactor.add(rule{secret: dir, replacement: dirReplacement})
}

// lineBuffer receives exactly one Write per record from slog.TextHandler.
type lineBuffer struct {
	mu    sync.Mutex
	lines []string
	limit int
}

func (b *lineBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, strings.TrimSuffix(string(p), "\n"))
	if len(b.lines) > b.limit {
		b.lines = b.lines[len(b.lines)-b.limit:]
	}
	return len(p), nil
}

func (b *lineBuffer) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.lines...)
}

// redactor removes device serial numbers and personal paths, which
// SECURITY.md asks reporters to redact. Paths match without regard to case,
// as Windows and default macOS volumes resolve them, and with either slash.
type redactor struct {
	mu    sync.RWMutex
	rules []rule // longest secret first, so a folder inside home hides fully
}

// rule replaces one secret, such as a personal path, with a placeholder.
type rule struct{ secret, replacement string }

// add records next, once in each slash direction.
func (r *redactor) add(next rule) {
	if next.secret == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range []string{next.secret, strings.ReplaceAll(next.secret, `\`, "/"), strings.ReplaceAll(next.secret, "/", `\`)} {
		if !slices.ContainsFunc(r.rules, func(existing rule) bool { return strings.EqualFold(existing.secret, s) }) {
			r.rules = append(r.rules, rule{secret: s, replacement: next.replacement})
		}
	}
	slices.SortStableFunc(r.rules, func(a, b rule) int { return len(b.secret) - len(a.secret) })
}

func (r *redactor) text(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, rl := range r.rules {
		s = rl.apply(s)
	}
	return s
}

// attr is the History's slog ReplaceAttr function. It also sees the built-in
// message attribute, so localized messages are redacted too.
func (r *redactor) attr(_ []string, a slog.Attr) slog.Attr {
	if a.Key == SerialKey {
		return slog.String(a.Key, redacted)
	}
	switch value := a.Value.Resolve(); value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, r.text(value.String()))
	case slog.KindAny:
		// Errors and other formatted values may embed paths.
		formatted := fmt.Sprint(value.Any())
		if redactedText := r.text(formatted); redactedText != formatted {
			return slog.String(a.Key, redactedText)
		}
	}
	return a
}

// apply replaces every case-insensitive occurrence of the secret in s.
func (rl rule) apply(s string) string {
	old := rl.secret
	var b strings.Builder
	last := 0
	for i := 0; i+len(old) <= len(s); {
		if strings.EqualFold(s[i:i+len(old)], old) {
			b.WriteString(s[last:i])
			b.WriteString(rl.replacement)
			i += len(old)
			last = i
			continue
		}
		i++
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}
