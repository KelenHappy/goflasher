//go:build windows

package disk

import "testing"

func TestPhysicalNumber(t *testing.T) {
	if got := physicalNumber(`\\.\PhysicalDrive42`); got != 42 {
		t.Fatalf("got %d", got)
	}
}
