//go:build darwin

package native

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Disk Arbitration replays every present disk, disk0 included, as appeared,
// so a watch reports at least once without any hardware being attached.
func TestWatchDisksReportsPresentDisksAndStopsOnCancel(t *testing.T) {
	_, s := openTestSession(t)
	defer s.Close()
	var calls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.WatchDisks(ctx, func() { calls.Add(1) }) }()

	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WatchDisks = %v, want nil after cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WatchDisks did not return after cancel")
	}
	if calls.Load() == 0 {
		t.Fatal("no appeared notification for the disks already present")
	}
	diskWatchers.Range(func(token, _ any) bool {
		t.Fatalf("watcher %v outlived its watch", token)
		return false
	})
}

func TestWatchDisksReturnsAtOnceWhenAlreadyCancelled(t *testing.T) {
	_, s := openTestSession(t)
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.WatchDisks(ctx, func() { t.Error("changed called for a cancelled watch") }); err != nil {
		t.Fatal(err)
	}
}
