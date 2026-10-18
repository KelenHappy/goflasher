package wim

import (
	"slices"
	"strings"
	"testing"
)

func TestDISMProgressParsesRedrawnBar(t *testing.T) {
	var got []uint64
	p := &dismProgress{report: func(permille uint64) { got = append(got, permille) }}
	output := "\r\nDeployment Image Servicing and Management tool\r\nVersion: 10.0.26100.1\r\n\r\nSplitting image\r\n" +
		"[                           0.0%                           ]\r" +
		"[====                       8.0%                           ]\r" +
		"[====                       8.0%                           ]\r" + // redraw without change
		"[==========================100.0%==========================]\r\n" +
		"The operation completed successfully. 50% of nothing.\r\n"
	// Deliver the output in awkward fragments, as a pipe may.
	for chunk := range slices.Chunk([]byte(output), 7) {
		if _, err := p.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if want := []uint64{80, 1000}; !slices.Equal(got, want) {
		t.Fatalf("progress=%v, want %v", got, want)
	}
}

func TestDISMProgressBoundsBufferedLine(t *testing.T) {
	p := &dismProgress{report: func(uint64) { t.Fatal("garbage reported as progress") }}
	if _, err := p.Write([]byte(strings.Repeat("x", 10*maxDISMLine))); err != nil {
		t.Fatal(err)
	}
	if len(p.line) != maxDISMLine {
		t.Fatalf("buffered line=%d bytes, want %d", len(p.line), maxDISMLine)
	}
	_, _ = p.Write([]byte("\n"))
}

func TestParseDISMPercentRejectsMalformedBars(t *testing.T) {
	for _, line := range []string{"", "50.0%", "[ no percent ]", "[ 150.0% ]", "[ . % ]", "[ 1.2.3% ]"} {
		if got, ok := parseDISMPercent([]byte(line)); ok {
			t.Errorf("parseDISMPercent(%q) = %d, want rejection", line, got)
		}
	}
}

func TestTailBufferKeepsLastBytes(t *testing.T) {
	tail := &tailBuffer{limit: 8}
	for _, s := range []string{"0123", "456789", "ab"} {
		_, _ = tail.Write([]byte(s))
	}
	if got := string(tail.data); got != "456789ab" {
		t.Fatalf("tail=%q", got)
	}
}
