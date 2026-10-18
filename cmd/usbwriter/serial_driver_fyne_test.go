//go:build fyne && (linux || windows || darwin)

package main

import (
	"sync"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

// mainLoop stands in for the real driver's main goroutine, which tests play
// themselves: fyne.Do from a background goroutine only queues its callback,
// and the test goroutine runs the queue whenever it calls fyne.DoAndWait, as
// waitFor does, or onGUI. Fyne's test driver instead runs every callback
// inline on the calling goroutine, so callbacks from background work race
// with each other and with the test, as the race detector reports.
//
// The application itself only calls fyne.Do, never fyne.DoAndWait, so in
// tests fyne.DoAndWait always comes from the test goroutine.
type mainLoop struct {
	mu    sync.Mutex
	queue []func()
}

var testMainLoop mainLoop

// post queues f without blocking, so a producer of many progress updates
// never stalls on a test that is not currently running the queue.
func (l *mainLoop) post(f func()) {
	l.mu.Lock()
	l.queue = append(l.queue, f)
	l.mu.Unlock()
}

// run executes queued callbacks, including ones they queue, until none remain.
func (l *mainLoop) run() {
	for {
		l.mu.Lock()
		if len(l.queue) == 0 {
			l.mu.Unlock()
			return
		}
		f := l.queue[0]
		l.queue = l.queue[1:]
		l.mu.Unlock()
		f()
	}
}

type serialDriver struct{ fyne.Driver }

func (serialDriver) DoFromGoroutine(f func(), wait bool) {
	if !wait {
		testMainLoop.post(f)
		return
	}
	testMainLoop.run()
	f()
}

// serialApp is a test app whose driver is a serialDriver.
type serialApp struct{ fyne.App }

func (a serialApp) Driver() fyne.Driver { return serialDriver{a.App.Driver()} }

// newSerialTestApp installs a test app with a serialDriver as the current app.
func newSerialTestApp(t *testing.T) fyne.App {
	t.Helper()
	inner := test.NewApp()
	a := serialApp{inner}
	fyne.SetCurrentApp(a)
	t.Cleanup(func() {
		testMainLoop.run()
		inner.Quit()
	})
	return a
}

// onGUI runs the callbacks background work has queued, then f, on the test
// goroutine.
func onGUI(f func()) { serialDriver{}.DoFromGoroutine(f, true) }
