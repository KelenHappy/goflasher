package app

import (
	"fmt"
	"sync"
)

type State string

const (
	Idle           State = "Idle"
	ImageSelected  State = "ImageSelected"
	DeviceSelected State = "DeviceSelected"
	Ready          State = "Ready"
	Confirming     State = "Confirming"
	Unmounting     State = "Unmounting"
	Writing        State = "Writing"
	Flushing       State = "Flushing"
	Verifying      State = "Verifying"
	Ejecting       State = "Ejecting"
	Completed      State = "Completed"
	Cancelled      State = "Cancelled"
	Failed         State = "Failed"
)

var transitions = map[State]map[State]bool{
	Idle:           {ImageSelected: true, DeviceSelected: true},
	ImageSelected:  {Idle: true, Ready: true},
	DeviceSelected: {Idle: true, Ready: true},
	Ready:          {Confirming: true, Idle: true},
	Confirming:     {Ready: true, Unmounting: true, Cancelled: true, Failed: true},
	Unmounting:     {Writing: true, Cancelled: true, Failed: true},
	Writing:        {Flushing: true, Cancelled: true, Failed: true},
	Flushing:       {Verifying: true, Ejecting: true, Completed: true, Failed: true},
	Verifying:      {Ejecting: true, Completed: true, Cancelled: true, Failed: true},
	Ejecting:       {Completed: true, Failed: true},
	Completed:      {Idle: true},
	Cancelled:      {Idle: true, Ready: true},
	Failed:         {Idle: true, Ready: true},
}

type StateMachine struct {
	mu    sync.RWMutex
	state State
}

func NewStateMachine() *StateMachine { return &StateMachine{state: Idle} }
func (s *StateMachine) State() State { s.mu.RLock(); defer s.mu.RUnlock(); return s.state }
func (s *StateMachine) Transition(next State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !transitions[s.state][next] {
		return fmt.Errorf("invalid state transition %s -> %s", s.state, next)
	}
	s.state = next
	return nil
}
