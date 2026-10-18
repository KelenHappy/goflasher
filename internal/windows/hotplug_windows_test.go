//go:build windows

package windows

import (
	"context"
	"testing"
	"unsafe"
)

// CM_NOTIFY_FILTER is 416 bytes on 64-bit Windows: four DWORDs, then a union
// whose largest member is WCHAR InstanceId[200].
func TestCMNotifyFilterMatchesCLayout(t *testing.T) {
	var filter cmNotifyFilter
	if got := unsafe.Sizeof(filter); got != 416 {
		t.Fatalf("sizeof(CM_NOTIFY_FILTER)=%d, want 416", got)
	}
	if got := unsafe.Offsetof(filter.classGUID); got != 16 {
		t.Fatalf("offsetof(u.DeviceInterface.ClassGuid)=%d, want 16", got)
	}
}

// Registration needs no device or elevation, so the real cfgmgr32 calls run
// here; only the arrival and removal callbacks need hardware.
func TestWatchDevicesRegistersAndUnregisters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := new(Backend).WatchDevices(ctx, func() {}); err != nil {
		t.Fatal(err)
	}
	var registered int
	hotplugWatchers.Range(func(any, any) bool { registered++; return true })
	if registered != 0 {
		t.Fatalf("watchers left registered=%d, want 0", registered)
	}
}

func TestHotplugNotifyForwardsOnlyInterfaceArrivalAndRemoval(t *testing.T) {
	calls := 0
	watcher := nextHotplugWatcher.Add(1)
	hotplugWatchers.Store(watcher, func() { calls++ })
	defer hotplugWatchers.Delete(watcher)
	for _, action := range []uintptr{cmNotifyActionDeviceInterfaceArrival, cmNotifyActionDeviceInterfaceRemoval, 2 /* DEVICEQUERYREMOVE */} {
		hotplugNotify(0, watcher, action, 0, 0)
	}
	hotplugNotify(0, watcher+1, cmNotifyActionDeviceInterfaceArrival, 0, 0) // unknown watcher
	if calls != 2 {
		t.Fatalf("changed calls=%d, want 2", calls)
	}
}
