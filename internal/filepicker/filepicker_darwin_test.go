//go:build darwin

package filepicker

import "testing"

func TestDarwinPickerHasStableSignature(t *testing.T) {
	var picker func(string, string, string) (string, error) = OpenImage
	if picker == nil {
		t.Fatal("OpenImage is nil")
	}
}
