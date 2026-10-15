// Package filepicker opens the operating system's native file chooser.
package filepicker

import (
	"fmt"
	"net/url"
	"path/filepath"
)

func localPath(files []string) (string, error) {
	if len(files) == 0 {
		return "", nil
	}
	uri, err := url.Parse(files[0])
	if err != nil {
		return "", fmt.Errorf("parse selected file URI: %w", err)
	}
	if !isLocalFileURI(uri) {
		return "", fmt.Errorf("file chooser returned a non-local URI: %s", uri.Redacted())
	}
	return filepath.FromSlash(uri.Path), nil
}

// isLocalFileURI accepts file:///path and file://localhost/path only.
func isLocalFileURI(uri *url.URL) bool {
	if uri.Scheme != "file" {
		return false
	}
	return uri.Host == "" || uri.Host == "localhost"
}
