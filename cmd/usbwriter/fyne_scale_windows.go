//go:build fyne && windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	monitorDefaultToPrimary = 1
	scaleFactorPercent      = 100.0
)

var (
	shcore                       = windows.NewLazySystemDLL("shcore.dll")
	procGetScaleFactorForMonitor = shcore.NewProc("GetScaleFactorForMonitor")
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procMonitorFromPoint         = user32.NewProc("MonitorFromPoint")
)

// systemScaleFactor reports the primary monitor's desktop scaling, which is the
// factor Fyne multiplies into every window scale on Windows.
//
// GetScaleFactorForMonitor is the query used rather than GetDpiForMonitor or
// GetDpiForSystem because those two report 96 DPI until the process declares
// itself DPI aware, and this runs before the GUI driver does that. A scale of 1
// is returned whenever the display cannot be queried, leaving the chosen ratio
// untouched rather than guessing.
func systemScaleFactor() float64 {
	if procMonitorFromPoint.Find() != nil || procGetScaleFactorForMonitor.Find() != nil {
		return 1
	}
	// POINT is two LONGs packed into a single 64-bit argument; the origin picks
	// the primary monitor together with MONITOR_DEFAULTTOPRIMARY.
	monitor, _, _ := procMonitorFromPoint.Call(0, monitorDefaultToPrimary)
	if monitor == 0 {
		return 1
	}
	var percent uint32
	result, _, _ := procGetScaleFactorForMonitor.Call(monitor, uintptr(unsafe.Pointer(&percent)))
	if result != 0 || percent == 0 {
		return 1
	}
	return float64(percent) / scaleFactorPercent
}
