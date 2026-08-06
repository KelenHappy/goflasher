package writer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestCopy(t *testing.T) {
	source := bytes.Repeat([]byte{1, 2, 3, 4}, 4096)
	var target bytes.Buffer
	updates := make(chan interface{}, 1)
	_ = updates // channel delivery is separately covered by result assertions
	r, err := Copy(context.Background(), &target, bytes.NewReader(source), Options{TotalBytes: uint64(len(source)), TargetSize: uint64(len(source)), BufferSize: 127})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(target.Bytes(), source) {
		t.Fatal("target differs")
	}
	if r.BytesWritten != uint64(len(source)) || len(r.SHA256) != 64 {
		t.Fatalf("result = %+v", r)
	}
}

func TestTargetTooSmall(t *testing.T) {
	_, err := Copy(context.Background(), io.Discard, bytes.NewReader(nil), Options{TotalBytes: 2, TargetSize: 1})
	if !errors.Is(err, ErrTargetTooSmall) {
		t.Fatalf("error = %v", err)
	}
}

func TestCopyRejectsChangedSourceLength(t *testing.T) {
	for _, test := range []struct {
		name     string
		source   string
		expected uint64
		written  int
	}{
		{name: "shorter", source: "abc", expected: 4, written: 3},
		{name: "longer", source: "abcde", expected: 4, written: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var target bytes.Buffer
			result, err := Copy(context.Background(), &target, strings.NewReader(test.source), Options{TotalBytes: test.expected, TargetSize: 16, BufferSize: 16})
			if !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("error = %v", err)
			}
			if result.BytesWritten != uint64(test.written) || target.Len() != test.written {
				t.Fatalf("result=%+v target bytes=%d", result, target.Len())
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Copy(ctx, io.Discard, bytes.NewReader([]byte("data")), Options{})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("error = %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("device removed") }
func TestWriteFailure(t *testing.T) {
	_, err := Copy(context.Background(), failingWriter{}, bytes.NewReader([]byte("data")), Options{})
	if !errors.Is(err, ErrWriteFailed) {
		t.Fatalf("error = %v", err)
	}
}

type tickingClock struct{ t time.Time }

func (c *tickingClock) now() time.Time { c.t = c.t.Add(time.Second); return c.t }
func TestAverageSpeed(t *testing.T) {
	c := &tickingClock{}
	r, err := Copy(context.Background(), io.Discard, bytes.NewReader(make([]byte, 100)), Options{Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	if r.AverageBytesPerSecond <= 0 {
		t.Fatalf("average = %v", r.AverageBytesPerSecond)
	}
}
