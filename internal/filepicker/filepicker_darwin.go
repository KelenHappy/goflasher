//go:build darwin

package filepicker

import (
	"fmt"
	"os/exec"
	"strings"
)

// OpenImage opens macOS's native Finder file chooser through AppleScript. An
// empty path and nil error mean that the user cancelled the panel. The panel's
// Open button is localized and controlled by macOS, so acceptLabel and
// filterName are intentionally unused; image.Detect validates the selection.
func OpenImage(title, acceptLabel, filterName string) (string, error) {
	_, _ = acceptLabel, filterName
	script := `try
POSIX path of (choose file with prompt "` + appleScriptString(title) + `")
on error number -128
return ""
end try`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return "", fmt.Errorf("open macOS file chooser: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func appleScriptString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
