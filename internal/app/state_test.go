package app

import "testing"

func TestStateMachine(t *testing.T) {
	s := NewStateMachine()
	valid := []State{ImageSelected, Ready, Confirming, Unmounting, Writing, Flushing, Verifying, Ejecting, Completed, Idle}
	for _, next := range valid {
		if err := s.Transition(next); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Transition(Completed); err == nil {
		t.Fatal("invalid transition accepted")
	}
}
