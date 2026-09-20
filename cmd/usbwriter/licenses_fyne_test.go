//go:build fyne && (linux || windows || darwin)

package main

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/goflasher/goflasher/internal/legal"
)

// TestLicenseAccordionBuildsPromptly guards the cost of showing the dialog.
// Every embedded text becomes an accordion entry, and the largest is the full
// GPL, so a regression that eagerly lays out all bodies would stall the UI
// thread on the click that opens settings.
func TestLicenseAccordionBuildsPromptly(t *testing.T) {
	test.NewApp()
	documents := legal.Documents()
	start := time.Now()
	items := make([]*widget.AccordionItem, 0, len(documents))
	for _, document := range documents {
		items = append(items, widget.NewAccordionItem(document.Title, licenseDetail(document.Body)))
	}
	accordion := widget.NewAccordion(items...)
	window := test.NewWindow(accordion)
	defer window.Close()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("building %d entries took %s, want under 2s", len(documents), elapsed)
	}
	for _, item := range accordion.Items {
		if item.Open {
			t.Errorf("entry %q starts expanded; entries must start collapsed", item.Title)
		}
	}
}

// TestLicenseDialogSizeKeepsAFloor covers the small-window case, where a
// proportional size alone would produce a dialog too small to read.
func TestLicenseDialogSizeKeepsAFloor(t *testing.T) {
	small := licenseDialogSize(fyne.NewSize(200, 150))
	if small.Width != 480 || small.Height != 400 {
		t.Errorf("small window gave %v, want the 480x400 floor", small)
	}
	large := licenseDialogSize(fyne.NewSize(1000, 1000))
	if large.Width != 900 || large.Height != 900 {
		t.Errorf("large window gave %v, want 90%% of the canvas", large)
	}
}
