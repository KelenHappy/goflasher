package wim

import (
	"slices"
	"testing"
)

func TestPercentProgressIsMonotonicAndEndsAtTotal(t *testing.T) {
	type report struct{ done, total uint64 }
	var got []report
	p := percentProgress{total: 1001, report: func(done, total uint64) { got = append(got, report{done, total}) }}
	for _, pct := range []uint64{0, 10, 10, 5, 50, 250} {
		p.percent(pct)
	}
	p.finish()
	p.finish()
	want := []report{{0, 1001}, {100, 1001}, {500, 1001}, {1001, 1001}}
	if !slices.Equal(got, want) {
		t.Fatalf("reports = %v, want %v", got, want)
	}
}

func TestPercentProgressIgnoresEmptySource(t *testing.T) {
	p := percentProgress{report: func(uint64, uint64) { t.Fatal("unexpected report") }}
	p.percent(50)
	p.finish()
}
