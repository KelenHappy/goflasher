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
	if uri.Scheme != "file" || (uri.Host != "" && uri.Host != "localhost") {
		return "", fmt.Errorf("file chooser returned a non-local URI: %s", uri.Redacted())
	}
	return filepath.FromSlash(uri.Path), nil
}
