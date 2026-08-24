//go:build darwin

package native

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDiskCollectorKeepsLatestDescriptionByBSDName(t *testing.T) {
	collector := newDiskCollector()
	defer collector.close()
	results := make(chan callbackResult, 2)
	results <- callbackResult{disk: DiskDescription{BSDName: "disk2", MediaName: "old"}}
	results <- callbackResult{disk: DiskDescription{BSDName: "disk2", MediaName: "new"}}

	for range 2 {
		if disks, err, done := collector.poll(context.Background(), results); done || err != nil || disks != nil {
			t.Fatalf("poll returned disks=%+v err=%v done=%t before quiet interval", disks, err, done)
		}
	}
	collector.quiet.Reset(0)
	disks, err, done := collector.poll(context.Background(), results)
	if !done || err != nil {
		t.Fatalf("completed poll returned err=%v done=%t", err, done)
	}
	if len(disks) != 1 || disks[0].MediaName != "new" {
		t.Fatalf("disks=%+v, want latest disk2 description", disks)
	}
}

func TestDiskCollectorReturnsCallbackError(t *testing.T) {
	want := errors.New("description failed")
	collector := newDiskCollector()
	defer collector.close()
	results := make(chan callbackResult, 1)
	results <- callbackResult{err: want}

	disks, err, done := collector.poll(context.Background(), results)
	if !done || !errors.Is(err, want) || disks != nil {
		t.Fatalf("poll returned disks=%+v err=%v done=%t", disks, err, done)
	}
}

func TestDiskCollectorHonorsCancellation(t *testing.T) {
	collector := newDiskCollector()
	defer collector.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	disks, err, done := collector.poll(ctx, make(chan callbackResult))
	if !done || !errors.Is(err, context.Canceled) || disks != nil {
		t.Fatalf("poll returned disks=%+v err=%v done=%t", disks, err, done)
	}
}

func TestResetTimerExtendsQuietInterval(t *testing.T) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	resetTimer(timer, time.Hour)
	select {
	case <-timer.C:
		t.Fatal("reset timer retained an expired signal")
	default:
	}
}
