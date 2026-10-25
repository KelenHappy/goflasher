//go:build linux && fyne

package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
)

// saveLogFile uses Fyne's bundled chooser, like openImage, which asks before
// overwriting an existing file. labels.Filter is unused because the chooser
// shows every file in the destination directory.
func saveLogFile(parent fyne.Window, labels logSaveLabels, contents []byte, done func(string, error)) {
	chooser := dialog.NewFileSave(func(file fyne.URIWriteCloser, err error) {
		if err != nil || file == nil {
			done("", err)
			return
		}
		_, err = file.Write(contents)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		done(file.URI().Path(), err)
	}, parent)
	chooser.SetFileName(labels.FileName)
	chooser.SetTitleText(labels.Title)
	chooser.SetConfirmText(labels.Accept)
	chooser.SetDismissText(labels.Dismiss)
	desiredSize := fyne.NewSize(em(fileDialogWidthEm), em(fileDialogHeightEm))
	// See openImage: Fyne 2.8 requires Show before Resize.
	chooser.Show()
	chooser.Resize(desiredSize)
	keepFileDialogSized(chooser, parent, desiredSize)
}
