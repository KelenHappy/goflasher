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
		assertPollPending(t, pollCollector(context.Background(), collector, results))
	}
	collector.quiet.Reset(0)
	got := pollCollector(context.Background(), collector, results)
	assertPollDone(t, got, nil)
	assertSingleDisk(t, got.disks, "new")
}

func TestDiskCollectorReturnsCallbackError(t *testing.T) {
	want := errors.New("description failed")
	collector := newDiskCollector()
	defer collector.close()
	results := make(chan callbackResult, 1)
	results <- callbackResult{err: want}

	assertPollFailed(t, pollCollector(context.Background(), collector, results), want)
}

func TestDiskCollectorHonorsCancellation(t *testing.T) {
	collector := newDiskCollector()
	defer collector.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assertPollFailed(t, pollCollector(ctx, collector, make(chan callbackResult)), context.Canceled)
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

// pollResult captures the values returned by diskCollector.poll.
type pollResult struct {
	disks []DiskDescription
	err   error
	done  bool
}

func pollCollector(ctx context.Context, collector *diskCollector, results <-chan callbackResult) pollResult {
	disks, err, done := collector.poll(ctx, results)
	return pollResult{disks: disks, err: err, done: done}
}

// assertPollPending checks that poll is still waiting for the quiet interval.
func assertPollPending(t *testing.T, got pollResult) {
	t.Helper()
	if got.done {
		t.Fatalf("poll returned %+v, want not done before quiet interval", got)
	}
	if got.err != nil {
		t.Fatalf("poll returned %+v, want no error before quiet interval", got)
	}
	if got.disks != nil {
		t.Fatalf("poll returned %+v, want no disks before quiet interval", got)
	}
}

// assertPollDone checks that poll finished with an error matching want.
func assertPollDone(t *testing.T, got pollResult, want error) {
	t.Helper()
	if !got.done {
		t.Fatalf("poll returned %+v, want done", got)
	}
	if !errors.Is(got.err, want) {
		t.Fatalf("poll returned %+v, want err %v", got, want)
	}
}

// assertPollFailed checks that poll finished with want and returned no disks.
func assertPollFailed(t *testing.T, got pollResult, want error) {
	t.Helper()
	assertPollDone(t, got, want)
	if got.disks != nil {
		t.Fatalf("poll returned %+v, want no disks", got)
	}
}

func assertSingleDisk(t *testing.T, disks []DiskDescription, wantMediaName string) {
	t.Helper()
	if len(disks) != 1 {
		t.Fatalf("disks=%+v, want exactly one description", disks)
	}
	if disks[0].MediaName != wantMediaName {
		t.Fatalf("disks=%+v, want media name %q", disks, wantMediaName)
	}
}
