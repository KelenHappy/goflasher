//go:build windows

package windows

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func stubLock(t *testing.T, attempts *int, errs ...error) {
	oldOpen, oldLock, oldDelay := openVolumeHandle, lockAndDismount, lockRetryDelay
	t.Cleanup(func() { openVolumeHandle, lockAndDismount, lockRetryDelay = oldOpen, oldLock, oldDelay })
	openVolumeHandle = func(string, uint32) (windows.Handle, error) { return windows.InvalidHandle, nil }
	lockRetryDelay = 0
	lockAndDismount = func(windows.Handle) error {
		*attempts++
		if *attempts <= len(errs) {
			return errs[*attempts-1]
		}
		return nil
	}
}

func TestLockVolumeRetriesTransientDenial(t *testing.T) {
	attempts := 0
	stubLock(t, &attempts, windows.ERROR_ACCESS_DENIED, windows.ERROR_SHARING_VIOLATION)
	if _, err := volumePath("\\\\?\\Volume{a}\\").lock(context.Background()); err != nil || attempts != 3 {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
}

func TestLockVolumeGivesUpAfterBoundedAttempts(t *testing.T) {
	attempts := 0
	denials := make([]error, lockVolumeAttempts+2)
	for i := range denials {
		denials[i] = windows.ERROR_ACCESS_DENIED
	}
	stubLock(t, &attempts, denials...)
	_, err := volumePath("\\\\?\\Volume{a}\\").lock(context.Background())
	requireErrorIs(t, err, ErrVolumeLockDenied)
	requireErrorIs(t, err, windows.ERROR_ACCESS_DENIED)
	requireEqual(t, "attempts", attempts, lockVolumeAttempts)
}

func TestLockVolumeDoesNotRetryPermanentFailure(t *testing.T) {
	attempts := 0
	stubLock(t, &attempts, windows.ERROR_NOT_SUPPORTED)
	_, err := volumePath("\\\\?\\Volume{a}\\").lock(context.Background())
	if !errors.Is(err, windows.ERROR_NOT_SUPPORTED) || attempts != 1 {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
}

func TestLockVolumeRetryHonorsCancellation(t *testing.T) {
	attempts := 0
	ctx, cancel := context.WithCancel(context.Background())
	oldOpen, oldLock, oldDelay := openVolumeHandle, lockAndDismount, lockRetryDelay
	t.Cleanup(func() { openVolumeHandle, lockAndDismount, lockRetryDelay = oldOpen, oldLock, oldDelay })
	openVolumeHandle = func(string, uint32) (windows.Handle, error) { return windows.InvalidHandle, nil }
	lockRetryDelay = time.Hour
	lockAndDismount = func(windows.Handle) error {
		attempts++
		cancel()
		return windows.ERROR_ACCESS_DENIED
	}
	_, err := volumePath("\\\\?\\Volume{a}\\").lock(ctx)
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
}

func TestHandleLocksCloseIsIdempotent(t *testing.T) {
	l := &handleLocks{}
	l.add(windows.InvalidHandle)
	_ = l.Close()
	if l.handles != nil {
		t.Fatal("handles retained after Close")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close error=%v", err)
	}
}
