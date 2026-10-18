package wim

import (
	"bytes"
	"strconv"
)

// The DISM backend is Windows-only, but its output parsing has no platform
// dependency and lives here so it is tested on every platform.

// maxDISMLine bounds one buffered line of DISM output; the progress bar is
// about 60 characters.
const maxDISMLine = 256

// dismProgress scans DISM's console output for its progress bar, such as
// "[==========                 25.0%                          ]", and reports
// each increase in tenths of a percent. DISM redraws the bar with carriage
// returns, so both CR and LF end a line.
type dismProgress struct {
	report func(permille uint64)
	line   []byte
	last   uint64
}

func (p *dismProgress) Write(b []byte) (int, error) {
	for _, c := range b {
		if c == '\r' || c == '\n' {
			p.endLine()
			continue
		}
		if len(p.line) < maxDISMLine {
			p.line = append(p.line, c)
		}
	}
	return len(b), nil
}

func (p *dismProgress) endLine() {
	permille, ok := parseDISMPercent(p.line)
	p.line = p.line[:0]
	if ok && permille > p.last {
		p.last = permille
		p.report(permille)
	}
}

// parseDISMPercent returns the percentage in a progress bar line in tenths of
// a percent.
func parseDISMPercent(line []byte) (uint64, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '[' {
		return 0, false
	}
	end := bytes.IndexByte(line, '%')
	if end < 0 {
		return 0, false
	}
	start := end
	for start > 0 && (line[start-1] == '.' || line[start-1] >= '0' && line[start-1] <= '9') {
		start--
	}
	percent, err := strconv.ParseFloat(string(line[start:end]), 64)
	if err != nil || percent < 0 || percent > 100 {
		return 0, false
	}
	return uint64(percent*10 + 0.5), true
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	limit int
	data  []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.data = append(t.data, b...)
	if over := len(t.data) - t.limit; over > 0 {
		t.data = append(t.data[:0], t.data[over:]...)
	}
	return len(b), nil
}
