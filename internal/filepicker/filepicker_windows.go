//go:build windows

package filepicker

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

const (
	ofOverwritePrompt  = 0x00000002
	ofNoChangeDir      = 0x00000008
	ofPathMustExist    = 0x00000800
	ofFileMustExist    = 0x00001000
	ofNoReadOnlyReturn = 0x00008000
	ofExplorer         = 0x00080000
	maxWindowsPath     = 32768
)

var (
	comdlg32             = syscall.NewLazyDLL("comdlg32.dll")
	getOpenFileNameW     = comdlg32.NewProc("GetOpenFileNameW")
	getSaveFileNameW     = comdlg32.NewProc("GetSaveFileNameW")
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
	dialog := openFileNameW{
		filter:      &filterUTF16[0],
		filterIndex: 1,
		title:       titleUTF16,
		flags:       ofExplorer | ofFileMustExist | ofPathMustExist | ofNoChangeDir,
	}
	return runFileDialog(getOpenFileNameW, &dialog, make([]uint16, maxWindowsPath), "open")
}

// SaveLog opens Windows' native Explorer save dialog with defaultName
// prefilled. Explorer asks before overwriting an existing file and appends
// .log when the user omits an extension. An empty path and a nil error mean
// that the user closed the dialog.
func SaveLog(title, defaultName, filterName string) (string, error) {
	titleUTF16, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return "", fmt.Errorf("encode save dialog title: %w", err)
	}
	name, err := syscall.UTF16FromString(defaultName)
	if err != nil {
		return "", fmt.Errorf("encode default log file name: %w", err)
	}
	defaultExtension, err := syscall.UTF16PtrFromString("log")
	if err != nil {
		return "", fmt.Errorf("encode default log extension: %w", err)
	}
	filterUTF16 := windowsLogFilter(filterName)
	file := make([]uint16, maxWindowsPath)
	copy(file[:len(file)-1], name)
	dialog := openFileNameW{
		filter:           &filterUTF16[0],
		filterIndex:      1,
		title:            titleUTF16,
		flags:            ofExplorer | ofOverwritePrompt | ofPathMustExist | ofNoReadOnlyReturn | ofNoChangeDir,
		defaultExtension: defaultExtension,
	}
	return runFileDialog(getSaveFileNameW, &dialog, file, "save")
}

// runFileDialog shows an Explorer common dialog that reads and writes the
// selected path through file.
func runFileDialog(proc *syscall.LazyProc, dialog *openFileNameW, file []uint16, verb string) (string, error) {
	dialog.file = &file[0]
	dialog.maxFile = uint32(len(file))
	dialog.structSize = uint32(unsafe.Sizeof(*dialog))

	result, _, _ := proc.Call(uintptr(unsafe.Pointer(dialog)))
	if result == 0 {
		code, _, _ := commDlgExtendedError.Call()
		if code == 0 {
			return "", nil
		}
		return "", fmt.Errorf("%s Windows file chooser: common dialog error 0x%04x", verb, code)
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

func windowsLogFilter(filterName string) []uint16 {
	value := filterName + " (*.log)\x00*.log\x00" +
		"All files (*.*)\x00*.*\x00\x00"
	return utf16.Encode([]rune(value))
}
