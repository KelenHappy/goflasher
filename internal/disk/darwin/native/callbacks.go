//go:build darwin

package native

import (
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
	select {
	case s.result <- callbackResult{d, e}:
	default:
	}
}
