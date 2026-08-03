//go:build windows

package filepicker

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

const (
	ofNoChangeDir   = 0x00000008
	ofPathMustExist = 0x00000800
	ofFileMustExist = 0x00001000
	ofExplorer      = 0x00080000
	maxWindowsPath  = 32768
)

var (
	comdlg32             = syscall.NewLazyDLL("comdlg32.dll")
	getOpenFileNameW     = comdlg32.NewProc("GetOpenFileNameW")
	commDlgExtendedError = comdlg32.NewProc("CommDlgExtendedError")
)

// openFileNameW mirrors the Win32 OPENFILENAMEW structure. uintptr keeps the
// pointer-sized fields correctly aligned on both 32-bit and 64-bit Windows.
type openFileNameW struct {
	structSize       uint32
	owner            uintptr
	instance         uintptr
	filter           *uint16
	customFilter     *uint16
	maxCustomFilter  uint32
	filterIndex      uint32
	file             *uint16
	maxFile          uint32
	fileTitle        *uint16
	maxFileTitle     uint32
	initialDir       *uint16
	title            *uint16
	flags            uint32
	fileOffset       uint16
	fileExtension    uint16
	defaultExtension *uint16
	customData       uintptr
	hook             uintptr
	templateName     *uint16
	reserved         uintptr
	reserved2        uint32
	flagsEx          uint32
}

// OpenImage opens Windows' native Explorer file chooser. An empty path and a
// nil error mean that the user closed the chooser. Windows controls the Open
// button label, so acceptLabel is intentionally unused on this platform.
func OpenImage(title, acceptLabel, filterName string) (string, error) {
	_ = acceptLabel

	titleUTF16, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return "", fmt.Errorf("encode file chooser title: %w", err)
	}
	filterUTF16 := windowsImageFilter(filterName)
	file := make([]uint16, maxWindowsPath)
	dialog := openFileNameW{
		filter:      &filterUTF16[0],
		filterIndex: 1,
		file:        &file[0],
		maxFile:     uint32(len(file)),
		title:       titleUTF16,
		flags:       ofExplorer | ofFileMustExist | ofPathMustExist | ofNoChangeDir,
	}
	dialog.structSize = uint32(unsafe.Sizeof(dialog))

	result, _, _ := getOpenFileNameW.Call(uintptr(unsafe.Pointer(&dialog)))
	if result == 0 {
		code, _, _ := commDlgExtendedError.Call()
		if code == 0 {
			return "", nil
		}
		return "", fmt.Errorf("open Windows file chooser: common dialog error 0x%04x", code)
	}
	return syscall.UTF16ToString(file), nil
}

func windowsImageFilter(filterName string) []uint16 {
	// Win32 filters are pairs of NUL-terminated display names and patterns,
	// terminated by a second NUL. Semicolon-separated patterns form one group.
	value := filterName + "\x00" +
		"*.iso;*.img;*.raw;*.iso.gz;*.img.gz;*.iso.xz;*.img.xz\x00" +
		"All files (*.*)\x00*.*\x00\x00"
	return utf16.Encode([]rune(value))
}
