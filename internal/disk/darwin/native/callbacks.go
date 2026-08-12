//go:build darwin

package native

import (
	"errors"
	"sync"
	"sync/atomic"
)

type callbackResult struct {
	disk DiskDescription
	err  error
}
type callbackState struct {
	token   uintptr
	session *Session
	result  chan callbackResult
	once    sync.Once
}

var nextCallbackToken atomic.Uint64
var callbackStates sync.Map

func newCallbackState(s *Session) *callbackState {
	v := nextCallbackToken.Add(1)
	if v == 0 {
		v = nextCallbackToken.Add(1)
	}
	x := &callbackState{token: uintptr(v), session: s, result: make(chan callbackResult, 1)}
	callbackStates.Store(x.token, x)
	return x
}
func (s *callbackState) close() { s.once.Do(func() { callbackStates.Delete(s.token) }) }
func dispatchAppeared(token, disk uintptr) {
	v, ok := callbackStates.Load(token)
	if !ok {
		return
	}
	s := v.(*callbackState)
	d, e := s.session.describe(disk)
	// Disk Arbitration can report volume-only objects which have a volume URL
	// but no BSD device name. They are not usable disks, so keep waiting for a
	// callback that describes an actual BSD device instead of failing the wait.
	if errors.Is(e, errDiskWithoutBSDName) {
		return
	}
	select {
	case s.result <- callbackResult{d, e}:
	default:
	}
}
