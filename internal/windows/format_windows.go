//go:build windows

package windows

import (
	"context"
	"errors"
	"os"

	"github.com/goflasher/goflasher/internal/fat32"
	"github.com/goflasher/goflasher/internal/progress"
	"golang.org/x/sys/windows"
)

type winFile struct {
	f   *os.File
	ctx context.Context
}

func (f *winFile) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.f.Read(p)
}

func (f *winFile) Write(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.f.Write(p)
}

func (f *winFile) WriteAt(p []byte, off int64) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.f.WriteAt(p, off)
}

func (f *winFile) Close() error { return f.f.Close() }

// Flush and Sync are the same operation: os.File.Sync is FlushFileBuffers on
// Windows. Both names exist to satisfy nativeFile and fat32.Device.
func (f *winFile) Flush() error { return f.f.Sync() }
func (f *winFile) Sync() error  { return f.Flush() }

func (a *winAPI) openDisk(ctx context.Context, r diskRecord, write bool) (nativeFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	access := uint32(windows.GENERIC_READ)
	if write {
		access |= windows.GENERIC_WRITE
	}
	h, err := openHandle(r.Path, access)
	if err != nil {
		return nil, err
	}
	fresh, err := inspectHandle(h, r.deviceNumber)
	if err != nil || diskRecordChanged(r, fresh) {
		windows.CloseHandle(h)
		if err != nil {
			return nil, err
		}
		return nil, ErrDeviceChanged
	}
	return &winFile{f: os.NewFile(uintptr(h), r.Path), ctx: ctx}, nil
}

func diskRecordChanged(selected, fresh diskRecord) bool {
	return !sameWindowsIdentity(fresh.identity, selected.identity) || fresh.ID != selected.ID || fresh.Size != selected.Size
}

func (a *winAPI) formatFAT32(ctx context.Context, r diskRecord, label string, updates chan<- progress.Update) error {
	f, err := a.openDisk(ctx, r, true)
	if err != nil {
		return err
	}
	defer f.Close()
	target, ok := f.(fat32.Device)
	if !ok {
		return errors.New("raw disk does not support random-access formatting")
	}
	// Progress is best effort: a full channel drops the update rather than
	// stalling the format, and cancellation is observed by fat32.Format itself.
	err = fat32.Format(ctx, target, r.Size, label, func(percent uint64) {
		if updates == nil {
			return
		}
		select {
		case updates <- progress.Update{Stage: progress.StageFormatting, BytesProcessed: percent, TotalBytes: 100}:
		default:
		}
	})
	if err != nil {
		return err
	}
	return target.Sync()
}
