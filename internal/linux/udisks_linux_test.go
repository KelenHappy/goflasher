//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type fakeUDisks struct {
	mountInfo  string
	unmounted  []string
	poweredOff []string
	err        error
}

func (f *fakeUDisks) Unmount(_ context.Context, devPath string) error {
	f.unmounted = append(f.unmounted, devPath)
	if f.err != nil {
		return f.err
	}
	return os.WriteFile(f.mountInfo, nil, 0600)
}

func (f *fakeUDisks) PowerOff(_ context.Context, devPath string) error {
	f.poweredOff = append(f.poweredOff, devPath)
	return f.err
}

func TestUnmountAllMountedPartitions(t *testing.T) {
	t.Run("unmounts every mounted partition", func(t *testing.T) {
		b := newBackendFixture(t)
		service := b.udisks.(*fakeUDisks)
		selected, err := b.RefreshDevice(context.Background(), "FLASH123")
		requireNoError(t, err)
		requireNoError(t, b.Unmount(context.Background(), selected))
		if data, _ := os.ReadFile(b.MountInfo); len(data) != 0 {
			t.Fatalf("mountinfo remains: %q", data)
		}
		requirePaths(t, "unmounted", service.unmounted,
			filepath.Join(b.DevRoot, "sdb1"), filepath.Join(b.DevRoot, "sdb2"))
	})
	t.Run("reports an unmount failure", func(t *testing.T) {
		b := newBackendFixture(t)
		b.udisks.(*fakeUDisks).err = fmt.Errorf("busy")
		selected, err := b.RefreshDevice(context.Background(), "FLASH123")
		requireNoError(t, err)
		if err := b.Unmount(context.Background(), selected); !errors.Is(err, ErrUnmountFailed) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestUDisksOperationsUseDirectDBusClient(t *testing.T) {
	t.Run("format unmounts through UDisks", func(t *testing.T) {
		b := newBackendFixture(t)
		service := b.udisks.(*fakeUDisks)
		fake := &fakePrivilegedHelper{}
		b.helper = fake
		selected, err := b.RefreshDevice(context.Background(), "FLASH123")
		requireNoError(t, err)
		requireNoError(t, b.FormatFAT32(context.Background(), selected, "GOFLASHER", nil))
		requirePaths(t, "unmounted", service.unmounted,
			filepath.Join(b.DevRoot, "sdb1"), filepath.Join(b.DevRoot, "sdb2"))
		assertFormatRequest(t, fake.requests, "GOFLASHER")
	})
	t.Run("eject powers off through UDisks", func(t *testing.T) {
		b := newBackendFixture(t)
		service := b.udisks.(*fakeUDisks)
		selected, err := b.RefreshDevice(context.Background(), "CARD123")
		requireNoError(t, err)
		requireNoError(t, b.Eject(context.Background(), selected))
		requirePaths(t, "powered-off", service.poweredOff, selected.Path)
	})
}

func requirePaths(t *testing.T, operation string, paths []string, want ...string) {
	t.Helper()
	requireSameStrings(t, operation+" devices", paths, want)
}

func assertFormatRequest(t *testing.T, requests []privilegedRequest, label string) {
	t.Helper()
	if len(requests) != 1 {
		t.Fatalf("format helper request count = %d, want 1: %#v", len(requests), requests)
	}
	if requests[0].Mode != modeFormatFAT32 {
		t.Fatalf("format helper mode = %q, want %q", requests[0].Mode, modeFormatFAT32)
	}
	if requests[0].Label != label {
		t.Fatalf("format helper label = %q, want %q", requests[0].Label, label)
	}
}
