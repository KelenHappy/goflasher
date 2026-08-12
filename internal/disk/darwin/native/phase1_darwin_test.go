//go:build darwin

package native

import (
	"context"
	"errors"
	"testing"
	"time"
	"unsafe"
)

func TestPhase1FrameworksSessionDescriptionAndIOKit(t *testing.T) {
	f, err := OpenFrameworks()
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d, err := s.DiskFromBSDName("disk0")
	if err != nil {
		t.Fatal(err)
	}
	if d.BSDName != "disk0" || d.Size == 0 {
		t.Fatalf("description=%+v", d)
	}
	id, err := f.RegistryIdentity("disk0")
	if err != nil {
		t.Fatal(err)
	}
	if id.EntryID == "" || id.Path == "" {
		t.Fatalf("identity=%+v", id)
	}
}

func TestPhase1CoreFoundationConversions(t *testing.T) {
	f, err := OpenFrameworks()
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.cf.newString("GoFlasher-測試")
	if err != nil {
		t.Fatal(err)
	}
	defer f.cf.api.release(s)
	if got, ok := f.cf.goString(s); !ok || got != "GoFlasher-測試" {
		t.Fatalf("string=%q,%v", got, ok)
	}
	want := int64(16000000000)
	n := f.cf.api.numberCreate(0, cfNumberSInt64Type, unsafe.Pointer(&want))
	if n == 0 {
		t.Fatal("CFNumberCreate returned NULL")
	}
	defer f.cf.api.release(n)
	if got, ok := f.cf.goUint64(n); !ok || got != uint64(want) {
		t.Fatalf("number=%d,%v", got, ok)
	}
}

func TestPhase1DiskArbitrationCallback(t *testing.T) {
	f, err := OpenFrameworks()
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.diagnostic = func(d descriptionDiagnostics) {
		t.Logf("DADiskRef=%#x DADiskGetBSDName=%q description=%#x description CFTypeID=%d CFDictionary type ID=%d dictionary count=%d", d.Disk, d.BSDName, d.Description, d.DescriptionTypeID, d.DictionaryTypeID, d.DictionaryCount)
		for _, key := range []string{"kDADiskDescriptionMediaBSDNameKey", "kDADiskDescriptionMediaNameKey", "kDADiskDescriptionMediaSizeKey", "kDADiskDescriptionMediaWholeKey", "kDADiskDescriptionDeviceInternalKey", "kDADiskDescriptionMediaEjectableKey", "kDADiskDescriptionMediaRemovableKey", "kDADiskDescriptionVolumePathKey"} {
			v := d.Values[key]
			t.Logf("%s: found=%t value CFTypeID=%d", key, v.Found, v.TypeID)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := s.WaitForDisk(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.BSDName == "" {
		t.Fatalf("callback description=%+v", d)
	}
}

func TestPhase1CallbackCancellation(t *testing.T) {
	f, err := OpenFrameworks()
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.WaitForDisk(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestPhase1CallbackExpiredDeadline(t *testing.T) {
	f, err := OpenFrameworks()
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = s.WaitForDisk(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
}
