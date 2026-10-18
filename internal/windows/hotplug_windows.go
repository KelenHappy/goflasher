//go:build windows

package windows

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/goflasher/goflasher/internal/device"
	"golang.org/x/sys/windows"
)

// CM_Register_Notification reports disk interface arrival and removal on a
// system thread-pool thread, so hotplug needs no window or message loop of its
// own; WM_DEVICECHANGE would need one, and Fyne owns the only window.
var (
	cmRegisterNotification   = cfgmgr.NewProc("CM_Register_Notification")
	cmUnregisterNotification = cfgmgr.NewProc("CM_Unregister_Notification")
)

const (
	cmNotifyFilterTypeDeviceInterface    = 0
	cmNotifyActionDeviceInterfaceArrival = 0
	cmNotifyActionDeviceInterfaceRemoval = 1
	crSuccess                            = 0
)

// cmNotifyFilter is CM_NOTIFY_FILTER with FilterType DEVICEINTERFACE. The C
// union is as large as its biggest member, WCHAR InstanceId[200], and cbSize
// must be the size of the whole structure.
type cmNotifyFilter struct {
	size, flags, filterType, reserved uint32
	classGUID                         windows.GUID
	_                                 [400 - unsafe.Sizeof(windows.GUID{})]byte
}

// Callbacks receive a watcher id, never a Go pointer, as their context.
var (
	hotplugCallbackOnce sync.Once
	hotplugCallback     uintptr
	hotplugWatchers     sync.Map // uintptr -> func()
	nextHotplugWatcher  atomic.Uintptr
)

// hotplugNotify is the PCM_NOTIFY_CALLBACK. It must return quickly.
func hotplugNotify(_, watcher, action, _, _ uintptr) uintptr {
	if action != cmNotifyActionDeviceInterfaceArrival && action != cmNotifyActionDeviceInterfaceRemoval {
		return uintptr(windows.ERROR_SUCCESS)
	}
	if changed, ok := hotplugWatchers.Load(watcher); ok {
		changed.(func())()
	}
	return uintptr(windows.ERROR_SUCCESS)
}

var _ device.HotplugBackend = (*Backend)(nil)

// WatchDevices follows disk interface arrival and removal.
func (b *Backend) WatchDevices(ctx context.Context, changed func()) error {
	hotplugCallbackOnce.Do(func() { hotplugCallback = windows.NewCallback(hotplugNotify) })
	watcher := nextHotplugWatcher.Add(1)
	hotplugWatchers.Store(watcher, changed)
	defer hotplugWatchers.Delete(watcher)
	filter := cmNotifyFilter{filterType: cmNotifyFilterTypeDeviceInterface, classGUID: diskInterfaceGUID}
	filter.size = uint32(unsafe.Sizeof(filter))
	var registration uintptr
	r, _, _ := cmRegisterNotification.Call(uintptr(unsafe.Pointer(&filter)), watcher, hotplugCallback, uintptr(unsafe.Pointer(&registration)))
	if r != crSuccess {
		return fmt.Errorf("CM_Register_Notification: CONFIGRET %#x", r)
	}
	<-ctx.Done()
	// Unregistering waits for callbacks already running to return.
	if r, _, _ := cmUnregisterNotification.Call(registration); r != crSuccess {
		return fmt.Errorf("CM_Unregister_Notification: CONFIGRET %#x", r)
	}
	return nil
}
