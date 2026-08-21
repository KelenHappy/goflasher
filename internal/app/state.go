package app

import (
	"fmt"
	"sync"
)

// State identifies a user-visible phase of the destructive write workflow.
type State string

const (
	// Idle indicates that neither a usable image nor a target is ready.
	Idle State = "Idle"
	// ImageSelected indicates that an image is ready but no target is selected.
	ImageSelected State = "ImageSelected"
	// DeviceSelected indicates that a target is ready but no image is selected.
	DeviceSelected State = "DeviceSelected"
	// Ready indicates that both an image and target are available.
	Ready State = "Ready"
	// Confirming indicates that destructive work is awaiting user confirmation.
	Confirming   State = "Confirming"
	Inspecting   State = "Inspecting"
	Planning     State = "Planning"
	StagingWIM   State = "StagingWIM"
	SplittingWIM State = "SplittingWIM"
	Partitioning State = "Partitioning"
	Formatting   State = "Formatting"
	Extracting   State = "Extracting"
	// Unmounting indicates that mounted target filesystems are being detached.
	Unmounting State = "Unmounting"
	// Writing indicates that image data is being copied to the target.
	Writing State = "Writing"
	// Flushing indicates that buffered target writes are becoming durable.
	Flushing State = "Flushing"
	// Verifying indicates that target data is being read back and checked.
	Verifying           State = "Verifying"
	VerifyingFilesystem State = "VerifyingFilesystem"
	// Ejecting indicates that the target is being released for safe removal.
	Ejecting State = "Ejecting"
	// Completed indicates that all requested workflow steps succeeded.
	Completed State = "Completed"
	// Cancelled indicates that context cancellation interrupted the workflow.
	Cancelled State = "Cancelled"
	// Failed indicates that a non-cancellation error stopped the workflow.
	Failed State = "Failed"
)

var transitions = map[State]map[State]bool{
	Idle:                {ImageSelected: true, DeviceSelected: true},
	ImageSelected:       {Idle: true, Ready: true},
	DeviceSelected:      {Idle: true, Ready: true},
	Ready:               {Confirming: true, Idle: true},
	Confirming:          {Ready: true, Inspecting: true, Unmounting: true, Cancelled: true, Failed: true},
	Inspecting:          {Planning: true, Cancelled: true, Failed: true},
	Planning:            {Unmounting: true, StagingWIM: true, Cancelled: true, Failed: true},
	Unmounting:          {Writing: true, Partitioning: true, Cancelled: true, Failed: true},
	StagingWIM:          {SplittingWIM: true, Cancelled: true, Failed: true},
	SplittingWIM:        {Unmounting: true, Cancelled: true, Failed: true},
	Partitioning:        {Formatting: true, Cancelled: true, Failed: true},
	Formatting:          {Extracting: true, Cancelled: true, Failed: true},
	Extracting:          {Flushing: true, Cancelled: true, Failed: true},
	Writing:             {Flushing: true, Cancelled: true, Failed: true},
	Flushing:            {Verifying: true, VerifyingFilesystem: true, Ejecting: true, Completed: true, Failed: true},
	Verifying:           {Ejecting: true, Completed: true, Cancelled: true, Failed: true},
	VerifyingFilesystem: {Ejecting: true, Completed: true, Cancelled: true, Failed: true},
	Ejecting:            {Completed: true, Failed: true},
	Completed:           {Idle: true},
	Cancelled:           {Idle: true, Ready: true},
	Failed:              {Idle: true, Ready: true},
}

// StateMachine serializes workflow transitions because the GUI and worker
// goroutines can observe or advance the same operation concurrently.
type StateMachine struct {
	mu    sync.RWMutex
	state State
}

// NewStateMachine returns a state machine in Idle.
func NewStateMachine() *StateMachine { return &StateMachine{state: Idle} }

// State returns a concurrency-safe snapshot of the current state.
func (s *StateMachine) State() State { s.mu.RLock(); defer s.mu.RUnlock(); return s.state }

// Transition advances to next only when the workflow permits that edge.
func (s *StateMachine) Transition(next State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !transitions[s.state][next] {
		return fmt.Errorf("invalid state transition %s -> %s", s.state, next)
	}
	s.state = next
	return nil
}
