//go:build darwin

package native

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/purego"
)

// watchSlice bounds how long the watching thread stays in its run loop before
// checking for cancellation.
const watchSlice = 0.25

// kCFRunLoopRunFinished: the run loop has no sources to wait on.
const runLoopRunFinished = 1

// Watchers are reached through a numeric token, never a Go pointer, like the
// other Disk Arbitration callbacks.
var (
	diskWatchers     sync.Map // uintptr -> func()
	nextWatchToken   atomic.Uint64
	watchAppeared    = purego.NewCallback(func(_, token uintptr) { notifyWatcher(token) })
	watchDisappeared = purego.NewCallback(func(_, token uintptr) { notifyWatcher(token) })
)

func notifyWatcher(token uintptr) {
	if changed, ok := diskWatchers.Load(token); ok {
		changed.(func())()
	}
}

// WatchDisks calls changed whenever Disk Arbitration reports a disk, partition
// or volume appearing or disappearing, until ctx is done. Registration replays
// every disk already present as appeared, so changed also runs for those.
// changed runs on the watching thread and must return quickly. The Session
// must not be used for anything else while it is being watched.
func (s *Session) WatchDisks(ctx context.Context, changed func()) error {
	if s == nil || s.ref == 0 {
		return ErrUnavailable
	}
	if ctx.Err() != nil {
		return nil
	}
	// Callbacks arrive on the run loop of the thread that scheduled the
	// session, so the goroutine must stay on that thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	token := uintptr(nextWatchToken.Add(1))
	if token == 0 {
		token = uintptr(nextWatchToken.Add(1))
	}
	diskWatchers.Store(token, changed)
	defer diskWatchers.Delete(token)

	da, cf := s.f.da.api, s.f.cf
	loop := cf.api.runLoopGetCurrent()
	da.registerAppeared(s.ref, 0, watchAppeared, token)
	da.registerDisappeared(s.ref, 0, watchDisappeared, token)
	da.scheduleRunLoop(s.ref, loop, cf.defaultRunLoopMode)
	defer func() {
		// Unschedule and unregister before the token is forgotten so no later
		// delivery reaches a finished watch.
		da.unscheduleRunLoop(s.ref, loop, cf.defaultRunLoopMode)
		da.unregisterCallback(s.ref, watchAppeared, token)
		da.unregisterCallback(s.ref, watchDisappeared, token)
	}()
	for ctx.Err() == nil {
		if cf.api.runLoopRunInMode(cf.defaultRunLoopMode, watchSlice, false) == runLoopRunFinished {
			time.Sleep(time.Duration(watchSlice * float64(time.Second)))
		}
	}
	return nil
}
