package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/image"
	"github.com/goflasher/goflasher/internal/progress"
	"github.com/goflasher/goflasher/internal/verify"
	"github.com/goflasher/goflasher/internal/writer"
)

type fileBackend struct {
	path                        string
	d                           device.Device
	unmounted, flushed, ejected bool
}

func (f *fileBackend) ListAllowedDevices(context.Context) ([]device.Device, error) {
	return []device.Device{f.d}, nil
}
func (f *fileBackend) RefreshDevice(context.Context, string) (device.Device, error) { return f.d, nil }
func (f *fileBackend) Unmount(context.Context, device.Device) error                 { f.unmounted = true; return nil }
func (f *fileBackend) OpenWriter(context.Context, device.Device) (io.WriteCloser, error) {
	return os.OpenFile(f.path, os.O_WRONLY|os.O_TRUNC, 0600)
}
func (f *fileBackend) OpenReader(context.Context, device.Device) (io.ReadCloser, error) {
	return os.Open(f.path)
}
func (f *fileBackend) Flush(context.Context, device.Device) error { f.flushed = true; return nil }
func (f *fileBackend) Eject(context.Context, device.Device) error { f.ejected = true; return nil }

type closeStateBackend struct {
	*fileBackend
	state        *StateMachine
	stateAtClose State
}

func (b *closeStateBackend) OpenWriter(context.Context, device.Device) (io.WriteCloser, error) {
	f, err := os.OpenFile(b.path, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	return &observedWriteCloser{WriteCloser: f, close: func() { b.stateAtClose = b.state.State() }}, nil
}

type observedWriteCloser struct {
	io.WriteCloser
	close func()
}

func (w *observedWriteCloser) Close() error {
	w.close()
	return w.WriteCloser.Close()
}

func TestServiceRawWriteVerifyEject(t *testing.T) {
	payload := bytes.Repeat([]byte("image"), 4096)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.img")
	target := filepath.Join(dir, "target")
	os.WriteFile(source, payload, 0600)
	os.WriteFile(target, make([]byte, len(payload)*2), 0600)
	info, err := image.Detect(source)
	if err != nil {
		t.Fatal(err)
	}
	d := device.Device{ID: "test", Path: target, Size: uint64(len(payload) * 2), IsAllowed: true}
	backend := &fileBackend{path: target, d: d}
	states := NewStateMachine()
	for _, s := range []State{ImageSelected, Ready, Confirming} {
		if err := states.Transition(s); err != nil {
			t.Fatal(err)
		}
	}
	service := Service{Backend: backend, State: states}
	result, err := service.Run(context.Background(), info, d, RunOptions{Verify: true, Eject: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || !result.Ejected || !backend.flushed || states.State() != Completed {
		t.Fatalf("result=%+v state=%s", result, states.State())
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, payload) {
		t.Fatal("written data differs")
	}
}

func TestServiceEntersFlushingBeforeWriterClose(t *testing.T) {
	payload := []byte("image payload")
	dir := t.TempDir()
	source := filepath.Join(dir, "source.iso")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, make([]byte, len(payload)), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := image.Detect(source)
	if err != nil {
		t.Fatal(err)
	}
	d := device.Device{ID: "test", Path: target, Size: uint64(len(payload)), IsAllowed: true}
	state := NewStateMachine()
	for _, next := range []State{ImageSelected, Ready, Confirming} {
		if err := state.Transition(next); err != nil {
			t.Fatal(err)
		}
	}
	backend := &closeStateBackend{fileBackend: &fileBackend{path: target, d: d}, state: state}
	if _, err := (&Service{Backend: backend, State: state}).Run(context.Background(), info, d, RunOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if backend.stateAtClose != Flushing {
		t.Fatalf("state at writer close = %s, want %s", backend.stateAtClose, Flushing)
	}
}

func TestServiceRejectsTooSmallBeforeUnmount(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "large.img")
	os.WriteFile(source, make([]byte, 32), 0600)
	info, _ := image.Detect(source)
	d := device.Device{ID: "test", Size: 16}
	backend := &fileBackend{d: d}
	states := NewStateMachine()
	states.Transition(ImageSelected)
	states.Transition(Ready)
	states.Transition(Confirming)
	_, err := (&Service{Backend: backend, State: states}).Run(context.Background(), info, d, RunOptions{}, nil)
	if err == nil || backend.unmounted {
		t.Fatalf("error=%v unmounted=%v", err, backend.unmounted)
	}
}

func TestServiceCancellationBeforeDestructiveWork(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.img")
	os.WriteFile(source, make([]byte, 1024), 0600)
	info, _ := image.Detect(source)
	d := device.Device{ID: "test", Size: 2048}
	backend := &fileBackend{d: d}
	states := NewStateMachine()
	states.Transition(ImageSelected)
	states.Transition(Ready)
	states.Transition(Confirming)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&Service{Backend: backend, State: states}).Run(ctx, info, d, RunOptions{}, nil)
	if err == nil || backend.unmounted || states.State() != Cancelled {
		t.Fatalf("error=%v unmounted=%v state=%s", err, backend.unmounted, states.State())
	}
}

func TestServiceRejectsSourceChecksumChangedAfterInspection(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.img")
	target := filepath.Join(dir, "target")
	payload := []byte("changed image")
	os.WriteFile(source, payload, 0600)
	os.WriteFile(target, make([]byte, len(payload)), 0600)
	info, err := image.Detect(source)
	if err != nil {
		t.Fatal(err)
	}
	info.UncompressedSize = uint64(len(payload))
	info.SHA256 = strings.Repeat("0", 64)
	d := device.Device{ID: "test", Path: target, Size: uint64(len(payload)), IsAllowed: true}
	backend := &fileBackend{path: target, d: d}
	states := NewStateMachine()
	for _, state := range []State{ImageSelected, Ready, Confirming} {
		if err := states.Transition(state); err != nil {
			t.Fatal(err)
		}
	}
	result, err := (&Service{Backend: backend, State: states}).Run(context.Background(), info, d, RunOptions{}, nil)
	if !errors.Is(err, writer.ErrSourceChanged) || result.BytesWritten != uint64(len(payload)) || backend.flushed {
		t.Fatalf("result=%+v error=%v flushed=%v", result, err, backend.flushed)
	}
}

type failureBackend struct {
	payload                           []byte
	unmountErr, openWriterErr         error
	writeErr, writerCloseErr          error
	flushErr, openReaderErr           error
	readErr, readerCloseErr, ejectErr error
	readerPayload                     []byte
	calls                             []string
	writer, reader                    *recordingWriteCloser
}

func (b *failureBackend) ListAllowedDevices(context.Context) ([]device.Device, error) {
	return nil, nil
}
func (b *failureBackend) RefreshDevice(context.Context, string) (device.Device, error) {
	return device.Device{}, nil
}
func (b *failureBackend) Unmount(context.Context, device.Device) error {
	b.calls = append(b.calls, "Unmount")
	return b.unmountErr
}
func (b *failureBackend) OpenWriter(context.Context, device.Device) (io.WriteCloser, error) {
	b.calls = append(b.calls, "OpenWriter")
	if b.openWriterErr != nil {
		return nil, b.openWriterErr
	}
	b.writer = &recordingWriteCloser{writeErr: b.writeErr, closeErr: b.writerCloseErr}
	return b.writer, nil
}
func (b *failureBackend) Flush(context.Context, device.Device) error {
	b.calls = append(b.calls, "Flush")
	return b.flushErr
}
func (b *failureBackend) OpenReader(context.Context, device.Device) (io.ReadCloser, error) {
	b.calls = append(b.calls, "OpenReader")
	if b.openReaderErr != nil {
		return nil, b.openReaderErr
	}
	payload := b.readerPayload
	if payload == nil {
		payload = b.payload
	}
	b.reader = &recordingWriteCloser{reader: bytes.NewReader(payload), readErr: b.readErr, closeErr: b.readerCloseErr}
	return b.reader, nil
}
func (b *failureBackend) Eject(context.Context, device.Device) error {
	b.calls = append(b.calls, "Eject")
	return b.ejectErr
}

type recordingWriteCloser struct {
	bytes.Buffer
	reader     *bytes.Reader
	writeErr   error
	readErr    error
	closeErr   error
	closeCalls int
}

func (c *recordingWriteCloser) Write(p []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.Buffer.Write(p)
}

func (c *recordingWriteCloser) Read(p []byte) (int, error) {
	if c.readErr != nil {
		err := c.readErr
		c.readErr = nil
		return 0, err
	}
	return c.reader.Read(p)
}

func (c *recordingWriteCloser) Close() error {
	c.closeCalls++
	return c.closeErr
}

func TestServiceStageFailures(t *testing.T) {
	payload := []byte("complete image payload")
	sourceHash := fmt.Sprintf("%x", sha256.Sum256(payload))
	mismatchPayload := bytes.Repeat([]byte("x"), len(payload))
	mismatchHash := fmt.Sprintf("%x", sha256.Sum256(mismatchPayload))
	errs := map[string]error{}
	for _, name := range []string{"unmount", "open writer", "write", "writer close", "flush", "open reader", "verify read", "reader close", "eject"} {
		errs[name] = fmt.Errorf("%s failure", name)
	}
	type expectedResult struct {
		written                uint64
		sourceHash, targetHash string
		verified, ejected      bool
	}
	tests := []struct {
		name                               string
		configure                          func(*failureBackend)
		opts                               RunOptions
		wantErr                            error
		wantPrefix                         string
		wantState                          State
		wantCalls                          []string
		wantWriterCloses, wantReaderCloses int
		want                               expectedResult
	}{
		{name: "Unmount", configure: func(b *failureBackend) { b.unmountErr = errs["unmount"] }, wantErr: errs["unmount"], wantState: Failed, wantCalls: []string{"Unmount"}},
		{name: "Unmount cancellation", configure: func(b *failureBackend) { b.unmountErr = context.Canceled }, wantErr: context.Canceled, wantState: Cancelled, wantCalls: []string{"Unmount"}},
		{name: "OpenWriter", configure: func(b *failureBackend) { b.openWriterErr = errs["open writer"] }, wantErr: errs["open writer"], wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter"}},
		{name: "writer Write", configure: func(b *failureBackend) { b.writeErr = errs["write"] }, wantErr: writer.ErrWriteFailed, wantPrefix: "write failed", wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter"}, wantWriterCloses: 1},
		{name: "writer Close", configure: func(b *failureBackend) { b.writerCloseErr = errs["writer close"] }, wantErr: errs["writer close"], wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter"}, wantWriterCloses: 1, want: expectedResult{written: uint64(len(payload)), sourceHash: sourceHash}},
		{name: "Flush", configure: func(b *failureBackend) { b.flushErr = errs["flush"] }, wantErr: errs["flush"], wantPrefix: "flush: ", wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter", "Flush"}, wantWriterCloses: 1, want: expectedResult{written: uint64(len(payload)), sourceHash: sourceHash}},
		{name: "OpenReader", configure: func(b *failureBackend) { b.openReaderErr = errs["open reader"] }, opts: RunOptions{Verify: true}, wantErr: errs["open reader"], wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter", "Flush", "OpenReader"}, wantWriterCloses: 1, want: expectedResult{written: uint64(len(payload)), sourceHash: sourceHash}},
		{name: "verify Read", configure: func(b *failureBackend) { b.readErr = errs["verify read"] }, opts: RunOptions{Verify: true}, wantErr: errs["verify read"], wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter", "Flush", "OpenReader"}, wantWriterCloses: 1, wantReaderCloses: 1, want: expectedResult{written: uint64(len(payload)), sourceHash: sourceHash}},
		{name: "verify checksum mismatch", configure: func(b *failureBackend) { b.readerPayload = mismatchPayload }, opts: RunOptions{Verify: true}, wantErr: verify.ErrMismatch, wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter", "Flush", "OpenReader"}, wantWriterCloses: 1, wantReaderCloses: 1, want: expectedResult{written: uint64(len(payload)), sourceHash: sourceHash, targetHash: mismatchHash}},
		{name: "reader Close", configure: func(b *failureBackend) { b.readerCloseErr = errs["reader close"] }, opts: RunOptions{Verify: true}, wantErr: errs["reader close"], wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter", "Flush", "OpenReader"}, wantWriterCloses: 1, wantReaderCloses: 1, want: expectedResult{written: uint64(len(payload)), sourceHash: sourceHash, targetHash: sourceHash}},
		{name: "Eject", configure: func(b *failureBackend) { b.ejectErr = errs["eject"] }, opts: RunOptions{Verify: true, Eject: true}, wantErr: errs["eject"], wantPrefix: "eject: ", wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter", "Flush", "OpenReader", "Eject"}, wantWriterCloses: 1, wantReaderCloses: 1, want: expectedResult{written: uint64(len(payload)), sourceHash: sourceHash, targetHash: sourceHash, verified: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.img")
			if err := os.WriteFile(source, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			backend := &failureBackend{payload: payload}
			tt.configure(backend)
			state := readyToRunState(t)
			info := image.Info{Path: source, Format: image.FormatIMG, Compression: image.CompressionNone, UncompressedSize: uint64(len(payload)), SHA256: sourceHash}
			target := device.Device{Size: uint64(len(payload))}
			got, err := (&Service{Backend: backend, State: state}).Run(context.Background(), info, target, tt.opts, nil)
			if !errors.Is(err, tt.wantErr) || tt.wantPrefix != "" && !strings.HasPrefix(err.Error(), tt.wantPrefix) {
				t.Fatalf("error = %v, want errors.Is(%v) and prefix %q", err, tt.wantErr, tt.wantPrefix)
			}
			if state.State() != tt.wantState {
				t.Errorf("state = %s, want %s", state.State(), tt.wantState)
			}
			if strings.Join(backend.calls, ",") != strings.Join(tt.wantCalls, ",") {
				t.Errorf("backend calls = %v, want %v", backend.calls, tt.wantCalls)
			}
			writerCloses, readerCloses := 0, 0
			if backend.writer != nil {
				writerCloses = backend.writer.closeCalls
			}
			if backend.reader != nil {
				readerCloses = backend.reader.closeCalls
			}
			if writerCloses != tt.wantWriterCloses || readerCloses != tt.wantReaderCloses {
				t.Errorf("close calls = writer %d, reader %d; want writer %d, reader %d", writerCloses, readerCloses, tt.wantWriterCloses, tt.wantReaderCloses)
			}
			if got.BytesWritten != tt.want.written || got.SourceSHA256 != tt.want.sourceHash || got.TargetSHA256 != tt.want.targetHash || got.Verified != tt.want.verified || got.Ejected != tt.want.ejected {
				t.Errorf("result = %+v, want completed fields %+v", got, tt.want)
			}
		})
	}
}

func TestSendStageDoesNotBlock(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() context.Context
	}{
		{name: "full updates channel", ctx: context.Background},
		{name: "cancelled context and full channel", ctx: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updates := make(chan progress.Update, 1)
			updates <- progress.Update{Stage: progress.StageWriting}
			done := make(chan struct{})
			go func() { sendStage(tt.ctx(), updates, progress.StageFlushing); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("sendStage blocked destructive workflow")
			}
		})
	}
}

func readyToRunState(t *testing.T) *StateMachine {
	t.Helper()
	state := NewStateMachine()
	for _, next := range []State{ImageSelected, Ready, Confirming} {
		if err := state.Transition(next); err != nil {
			t.Fatal(err)
		}
	}
	return state
}
