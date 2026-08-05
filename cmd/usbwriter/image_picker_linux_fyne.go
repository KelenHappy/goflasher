//go:build linux && fyne

package main

import (
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
)

const fileDialogResizePollInterval = 100 * time.Millisecond

// openImage uses only Fyne's bundled chooser on Linux. Image selection does not
// call XDG Desktop Portal, D-Bus, kdialog, Zenity, Dolphin, or Nautilus.
func openImage(parent fyne.Window, title, acceptLabel, dismissLabel, filterName string, done func(string, error)) {
	chooser := dialog.NewFileOpen(func(file fyne.URIReadCloser, err error) {
		if err != nil || file == nil {
			done("", err)
			return
		}
		path := file.URI().Path()
		closeErr := file.Close()
		if closeErr != nil {
			done("", closeErr)
			return
		}
		done(path, nil)
	}, parent)
	chooser.SetFilter(storage.NewExtensionFileFilter([]string{
		".iso", ".img", ".raw", ".iso.gz", ".img.gz", ".iso.xz", ".img.xz",
	}))
	chooser.SetTitleText(title)
	chooser.SetConfirmText(acceptLabel)
	chooser.SetDismissText(dismissLabel)
	desiredSize := fyne.NewSize(760, 520)
	// Fyne 2.8 initializes FileDialog's internal widget tree in Show. Calling
	// Resize first dereferences an uninitialized dialog in FileDialog.MinSize.
	chooser.Show()
	chooser.Resize(desiredSize)
	keepFileDialogSized(chooser, parent, desiredSize)
	_ = filterName // Fyne's extension filter does not expose a display name.
}

// keepFileDialogSized works around Fyne 2.8's modal overlay retaining the
// reduced dialog size after its parent canvas grows again.
func keepFileDialogSized(chooser *dialog.FileDialog, parent fyne.Window, desiredSize fyne.Size) {
	closed := make(chan struct{})
	var closeOnce sync.Once
	chooser.SetOnClosed(func() {
		closeOnce.Do(func() { close(closed) })
	})

	go func() {
		ticker := time.NewTicker(fileDialogResizePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-closed:
				return
			case <-ticker.C:
				fyne.Do(func() {
					canvasSize := parent.Canvas().Size()
					chooser.Resize(desiredSize.Min(canvasSize))
				})
			}
		}
	}()
}
