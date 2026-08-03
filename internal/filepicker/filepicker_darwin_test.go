//go:build darwin

package filepicker

import "testing"

func TestAppleScriptString(t *testing.T) {
	got := appleScriptString(`Choose "disk" from C:\images`)
	want := `Choose \"disk\" from C:\\images`
	if got != want {
		t.Fatalf("appleScriptString() = %q, want %q", got, want)
	}
}
