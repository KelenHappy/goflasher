//go:build linux

package filepicker

import "github.com/rymdport/portal/filechooser"

// OpenImage asks the desktop environment to select one disk image through the
// XDG Desktop Portal. An empty path and nil error mean the chooser was closed.
func OpenImage(title, acceptLabel, filterName string) (string, error) {
	filter := &filechooser.Filter{
		Name: filterName,
		Rules: []filechooser.Rule{
			{Type: filechooser.GlobPattern, Pattern: "*.iso"},
			{Type: filechooser.GlobPattern, Pattern: "*.ISO"},
			{Type: filechooser.GlobPattern, Pattern: "*.img"},
			{Type: filechooser.GlobPattern, Pattern: "*.IMG"},
			{Type: filechooser.GlobPattern, Pattern: "*.raw"},
			{Type: filechooser.GlobPattern, Pattern: "*.RAW"},
			{Type: filechooser.GlobPattern, Pattern: "*.img.gz"},
			{Type: filechooser.GlobPattern, Pattern: "*.IMG.GZ"},
			{Type: filechooser.GlobPattern, Pattern: "*.iso.gz"},
			{Type: filechooser.GlobPattern, Pattern: "*.ISO.GZ"},
			{Type: filechooser.GlobPattern, Pattern: "*.img.xz"},
			{Type: filechooser.GlobPattern, Pattern: "*.IMG.XZ"},
			{Type: filechooser.GlobPattern, Pattern: "*.iso.xz"},
			{Type: filechooser.GlobPattern, Pattern: "*.ISO.XZ"},
		},
	}
	files, err := filechooser.OpenFile("", title, &filechooser.OpenFileOptions{
		AcceptLabel:   acceptLabel,
		Filters:       []*filechooser.Filter{filter},
		CurrentFilter: filter,
	})
	if err != nil {
		return "", err
	}
	return localPath(files)
}
