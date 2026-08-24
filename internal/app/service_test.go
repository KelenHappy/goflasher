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
	"slices"
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

type releaseErrorBackend struct {
	*fileBackend
	err error
}

func (b *releaseErrorBackend) ReleaseDevice(device.Device) error { return b.err }

func TestServiceReportsDeviceReleaseFailure(t *testing.T) {
	payload := []byte("image payload")
	fixture := newFileServiceFixture(t, payload, 2)
	releaseErr := errors.New("release failed")
	backend := &releaseErrorBackend{fileBackend: fixture.backend, err: releaseErr}
	fixture.service.Backend = backend

	_, err := fixture.service.Run(context.Background(), fixture.info, fixture.device, RunOptions{}, nil)
	if !errors.Is(err, releaseErr) {
		t.Fatalf("Run() error = %v, want release error", err)
	}
	if got := fixture.state.State(); got != Failed {
		t.Fatalf("state = %s, want Failed", got)
	}
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
	fixture := newFileServiceFixture(t, payload, 2)
	result, err := fixture.service.Run(context.Background(), fixture.info, fixture.device, RunOptions{Verify: true, Eject: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified {
		t.Error("result was not verified")
	}
	if !result.Ejected {
		t.Error("result was not ejected")
	}
	if !fixture.backend.flushed {
		t.Error("backend was not flushed")
	}
	if fixture.state.State() != Completed {
		t.Errorf("state = %s, want %s", fixture.state.State(), Completed)
	}
	got, err := os.ReadFile(fixture.device.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("written data differs")
	}
}

type fileServiceFixture struct {
	service Service
	info    image.Info
	device  device.Device
	backend *fileBackend
	state   *StateMachine
}

func newFileServiceFixture(t *testing.T, payload []byte, targetMultiplier int) fileServiceFixture {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "source.img")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, make([]byte, len(payload)*targetMultiplier), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := image.Detect(source)
	if err != nil {
		t.Fatal(err)
	}
	d := device.Device{ID: "test", Path: target, Size: uint64(len(payload) * targetMultiplier), IsAllowed: true}
	backend := &fileBackend{path: target, d: d}
	state := readyToRunState(t)
	return fileServiceFixture{service: Service{Backend: backend, State: state}, info: info, device: d, backend: backend, state: state}
}

func TestServiceEntersFlushingBeforeWriterClose(t *testing.T) {
	payload := []byte("image payload")
	dir := t.TempDir()
	source := filepath.Join(dir, "source.img")
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
	if err == nil {
		t.Fatal("Run() succeeded after cancellation")
	}
	if backend.unmounted {
		t.Error("backend was unmounted after cancellation")
	}
	if states.State() != Cancelled {
		t.Errorf("state = %s, want %s", states.State(), Cancelled)
	}
}

func TestServiceRejectsMismatchedInspectedChecksumBeforeWrite(t *testing.T) {
	fixture := newFileServiceFixture(t, []byte("changed image"), 1)
	info, err := image.Inspect(fixture.info)
	if err != nil {
		t.Fatal(err)
	}
	info.SHA256 = strings.Repeat("0", 64)

	result, err := fixture.service.Run(context.Background(), info, fixture.device, RunOptions{}, nil)
	if err == nil {
		t.Fatal("mismatched inspected checksum was accepted")
	}
	if result.BytesWritten != 0 {
		t.Errorf("bytes written = %d, want 0", result.BytesWritten)
	}
	if fixture.backend.unmounted || fixture.backend.flushed {
		t.Error("target was prepared for a changed source")
	}
}

func TestServiceRejectsRetainedSourceChangeBeforeUnmount(t *testing.T) {
	fixture := newFileServiceFixture(t, []byte("original image"), 2)
	inspected, err := image.Inspect(fixture.info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(fixture.info.Path, 1); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.Run(context.Background(), inspected, fixture.device, RunOptions{}, nil)
	if err == nil {
		t.Fatal("changed retained source was accepted")
	}
	if fixture.backend.unmounted {
		t.Fatal("target was unmounted before changed source was rejected")
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

type expectedStageResult struct {
	written                uint64
	sourceHash, targetHash string
	verified, ejected      bool
}

type stageFailureCase struct {
	name                               string
	configure                          func(*failureBackend)
	opts                               RunOptions
	wantErr                            error
	wantPrefix                         string
	wantState                          State
	wantCalls                          []string
	wantWriterCloses, wantReaderCloses int
	want                               expectedStageResult
}

func TestServiceStageFailures(t *testing.T) {
	payload := []byte("complete image payload")
	sourceHash := fmt.Sprintf("%x", sha256.Sum256(payload))
	mismatchPayload := bytes.Repeat([]byte("x"), len(payload))
	mismatchHash := fmt.Sprintf("%x", sha256.Sum256(mismatchPayload))
	for _, testCase := range serviceStageFailureCases(payload, sourceHash, mismatchPayload, mismatchHash) {
		t.Run(testCase.name, func(t *testing.T) {
			runServiceStageFailure(t, testCase, payload)
		})
	}
}

func serviceStageFailureCases(payload []byte, sourceHash string, mismatchPayload []byte, mismatchHash string) []stageFailureCase {
	errs := stageErrors("unmount", "open writer", "write", "writer close", "flush", "open reader", "verify read", "reader close", "eject")
	written := uint64(len(payload))
	return []stageFailureCase{
		{name: "Unmount", configure: func(b *failureBackend) { b.unmountErr = errs["unmount"] }, wantErr: errs["unmount"], wantState: Failed, wantCalls: []string{"Unmount"}},
		{name: "Unmount cancellation", configure: func(b *failureBackend) { b.unmountErr = context.Canceled }, wantErr: context.Canceled, wantState: Cancelled, wantCalls: []string{"Unmount"}},
		{name: "OpenWriter", configure: func(b *failureBackend) { b.openWriterErr = errs["open writer"] }, wantErr: errs["open writer"], wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter"}},
		{name: "writer Write", configure: func(b *failureBackend) { b.writeErr = errs["write"] }, wantErr: writer.ErrWriteFailed, wantPrefix: "write failed", wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter"}, wantWriterCloses: 1},
		{name: "writer Close", configure: func(b *failureBackend) { b.writerCloseErr = errs["writer close"] }, wantErr: errs["writer close"], wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter"}, wantWriterCloses: 1, want: expectedStageResult{written: written, sourceHash: sourceHash}},
		{name: "Flush", configure: func(b *failureBackend) { b.flushErr = errs["flush"] }, wantErr: errs["flush"], wantPrefix: "flush: ", wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter", "Flush"}, wantWriterCloses: 1, want: expectedStageResult{written: written, sourceHash: sourceHash}},
		{name: "OpenReader", configure: func(b *failureBackend) { b.openReaderErr = errs["open reader"] }, opts: RunOptions{Verify: true}, wantErr: errs["open reader"], wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter", "Flush", "OpenReader"}, wantWriterCloses: 1, want: expectedStageResult{written: written, sourceHash: sourceHash}},
		{name: "verify Read", configure: func(b *failureBackend) { b.readErr = errs["verify read"] }, opts: RunOptions{Verify: true}, wantErr: errs["verify read"], wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter", "Flush", "OpenReader"}, wantWriterCloses: 1, wantReaderCloses: 1, want: expectedStageResult{written: written, sourceHash: sourceHash}},
		{name: "verify checksum mismatch", configure: func(b *failureBackend) { b.readerPayload = mismatchPayload }, opts: RunOptions{Verify: true}, wantErr: verify.ErrMismatch, wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter", "Flush", "OpenReader"}, wantWriterCloses: 1, wantReaderCloses: 1, want: expectedStageResult{written: written, sourceHash: sourceHash, targetHash: mismatchHash}},
		{name: "reader Close", configure: func(b *failureBackend) { b.readerCloseErr = errs["reader close"] }, opts: RunOptions{Verify: true}, wantErr: errs["reader close"], wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter", "Flush", "OpenReader"}, wantWriterCloses: 1, wantReaderCloses: 1, want: expectedStageResult{written: written, sourceHash: sourceHash, targetHash: sourceHash}},
		{name: "Eject", configure: func(b *failureBackend) { b.ejectErr = errs["eject"] }, opts: RunOptions{Verify: true, Eject: true}, wantErr: errs["eject"], wantPrefix: "eject: ", wantState: Failed, wantCalls: []string{"Unmount", "OpenWriter", "Flush", "OpenReader", "Eject"}, wantWriterCloses: 1, wantReaderCloses: 1, want: expectedStageResult{written: written, sourceHash: sourceHash, targetHash: sourceHash, verified: true}},
	}
}

func stageErrors(names ...string) map[string]error {
	result := make(map[string]error, len(names))
	for _, name := range names {
		result[name] = fmt.Errorf("%s failure", name)
	}
	return result
}

func runServiceStageFailure(t *testing.T, testCase stageFailureCase, payload []byte) {
	t.Helper()
	fixture := newFileServiceFixture(t, payload, 1)
	backend := &failureBackend{payload: payload}
	testCase.configure(backend)
	got, err := (&Service{Backend: backend, State: fixture.state}).Run(context.Background(), fixture.info, fixture.device, testCase.opts, nil)
	assertStageError(t, err, testCase)
	assertStageExecution(t, fixture.state, backend, testCase)
	assertStageResult(t, got, testCase.want)
}

func assertStageError(t *testing.T, err error, testCase stageFailureCase) {
	t.Helper()
	if !errors.Is(err, testCase.wantErr) {
		t.Errorf("error = %v, want errors.Is(%v)", err, testCase.wantErr)
	}
	if testCase.wantPrefix == "" {
		return
	}
	if err == nil || !strings.HasPrefix(err.Error(), testCase.wantPrefix) {
		t.Errorf("error = %v, want prefix %q", err, testCase.wantPrefix)
	}
}

func assertStageExecution(t *testing.T, state *StateMachine, backend *failureBackend, testCase stageFailureCase) {
	t.Helper()
	if state.State() != testCase.wantState {
		t.Errorf("state = %s, want %s", state.State(), testCase.wantState)
	}
	if !slices.Equal(backend.calls, testCase.wantCalls) {
		t.Errorf("backend calls = %v, want %v", backend.calls, testCase.wantCalls)
	}
	writerCloses := closeCalls(backend.writer)
	readerCloses := closeCalls(backend.reader)
	if writerCloses != testCase.wantWriterCloses {
		t.Errorf("writer close calls = %d, want %d", writerCloses, testCase.wantWriterCloses)
	}
	if readerCloses != testCase.wantReaderCloses {
		t.Errorf("reader close calls = %d, want %d", readerCloses, testCase.wantReaderCloses)
	}
}

func closeCalls(closer *recordingWriteCloser) int {
	if closer == nil {
		return 0
	}
	return closer.closeCalls
}

func assertStageResult(t *testing.T, got RunResult, want expectedStageResult) {
	t.Helper()
	if got.BytesWritten != want.written {
		t.Errorf("bytes written = %d, want %d", got.BytesWritten, want.written)
	}
	if got.SourceSHA256 != want.sourceHash {
		t.Errorf("source hash = %q, want %q", got.SourceSHA256, want.sourceHash)
	}
	if got.TargetSHA256 != want.targetHash {
		t.Errorf("target hash = %q, want %q", got.TargetSHA256, want.targetHash)
	}
	if got.Verified != want.verified {
		t.Errorf("verified = %v, want %v", got.Verified, want.verified)
	}
	if got.Ejected != want.ejected {
		t.Errorf("ejected = %v, want %v", got.Ejected, want.ejected)
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
