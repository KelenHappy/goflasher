//go:build fyne && (windows || darwin)

package main

import (
	"os"

	"fyne.io/fyne/v2"

	"github.com/goflasher/goflasher/internal/filepicker"
)

// saveLogFile ignores labels.Accept and labels.Dismiss because the operating
// system owns the native button labels. The native dialog has already asked
// before overwriting, so the file is replaced without further prompts.
func saveLogFile(_ fyne.Window, labels logSaveLabels, contents []byte, done func(string, error)) {
	runNativeDialog(func() (string, error) {
		path, err := filepicker.SaveLog(labels.Title, labels.FileName, labels.Filter)
		if err == nil && path != "" {
			err = os.WriteFile(path, contents, 0o644)
		}
		return path, err
	}, done)
}
