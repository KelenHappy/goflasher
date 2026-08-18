//go:build linux

package linux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/progress"
	"golang.org/x/sys/unix"
)

type fakePrivilegedHelper struct {
	requests []privilegedRequest
	err      error
	writes   strings.Builder
	readData string
}

func TestOpenedLinuxDeviceIsRevalidatedBeforeUse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *backendFixture, helperEnvironment)
		devno  uint64
		ok     bool
	}{
		{name: "normal", devno: unix.Mkdev(8, 16), ok: true},
		{name: "node number changed", devno: unix.Mkdev(8, 32)},
		{name: "identity changed", devno: unix.Mkdev(8, 16), mutate: func(t *testing.T, f *backendFixture, _ helperEnvironment) {
			write(t, filepath.Join(f.SysClassBlock, "sdb", "device/serial"), "REPLACED")
		}},
		{name: "device disappeared", devno: unix.Mkdev(8, 16), mutate: func(t *testing.T, _ *backendFixture, env helperEnvironment) {
			requireNoError(t, os.Remove(filepath.Join(env.SysDevBlock, "8:16")))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBackendFixture(t)
			clearFixtureActivity(t, f.Backend)
			sysDev := filepath.Join(f.t.TempDir(), "sys/dev/block")
			requireNoError(t, os.MkdirAll(sysDev, 0755))
			real, err := filepath.EvalSymlinks(filepath.Join(f.SysClassBlock, "sdb"))
			requireNoError(t, err)
			requireNoError(t, os.Symlink(real, filepath.Join(sysDev, "8:16")))
			env := helperEnvironment{SysDevBlock: sysDev, SysClassBlock: f.SysClassBlock, MountInfo: f.MountInfo, Swaps: f.Swaps, DevRoot: f.DevRoot}
			env.openFile = func(string, int, os.FileMode) (*os.File, error) {
				if tc.mutate != nil {
					tc.mutate(t, f, env)
				}
				return os.Open(filepath.Join(f.DevRoot, "sdb"))
			}
			env.fstat = func(_ int, st *syscall.Stat_t) error {
				st.Mode = syscall.S_IFBLK
				st.Rdev = tc.devno
				return nil
			}
			req := privilegedRequest{Identity: "FLASH123", Serial: "FLASH123", Major: 8, Minor: 16, Capacity: 65536 * 512, Mode: modeWrite}
			opened, err := openDevice(req, env, "sdb")
			if opened != nil {
				_ = opened.Close()
			}
			if (err == nil) != tc.ok {
				t.Fatalf("open error = %v, want success %t", err, tc.ok)
			}
		})
	}
}

func (f *fakePrivilegedHelper) OpenWriter(_ context.Context, r privilegedRequest) (io.WriteCloser, error) {
	f.requests = append(f.requests, r)
	if f.err != nil {
		return nil, f.err
	}
	return nopWriteCloser{&f.writes}, nil
}
func (f *fakePrivilegedHelper) OpenReader(_ context.Context, r privilegedRequest) (io.ReadCloser, error) {
	f.requests = append(f.requests, r)
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.readData)), nil
}
func (f *fakePrivilegedHelper) Flush(_ context.Context, r privilegedRequest) error {
	f.requests = append(f.requests, r)
	return f.err
}
func (f *fakePrivilegedHelper) FormatFAT32(_ context.Context, r privilegedRequest, _ chan<- progress.Update) error {
	f.requests = append(f.requests, r)
	return f.err
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

type syncBuffer struct {
	strings.Builder
	synced bool
}

func (b *syncBuffer) Sync() error { b.synced = true; return nil }

func TestRevalidationDetectsReplacement(t *testing.T) {
	b := newBackendFixture(t)
	selected, err := b.RefreshDevice(context.Background(), "FLASH123")
	if err != nil {
		t.Fatal(err)
	}
	b.replaceIdentity("sdb", "REPLACED", "ID_DRIVE_THUMB")
	if _, err := b.Revalidate(context.Background(), selected); !errors.Is(err, ErrDeviceChanged) {
		t.Fatalf("error = %v", err)
	}
}

func TestRawOperationsUseIdentityOnlyHelper(t *testing.T) {
	b := newBackendFixture(t)
	fake := &fakePrivilegedHelper{readData: "verified"}
	b.helper = fake
	selected, err := b.RefreshDevice(context.Background(), "CARD123")
	requireNoError(t, err)
	w, err := b.OpenWriter(context.Background(), selected)
	requireNoError(t, err)
	_, _ = io.WriteString(w, "image")
	_ = w.Close()
	r, err := b.OpenReader(context.Background(), selected)
	requireNoError(t, err)
	data, _ := io.ReadAll(r)
	_ = r.Close()
	requireNoError(t, b.Flush(context.Background(), selected))
	assertRawOperationResults(t, fake, data)
	assertHelperRequests(t, fake.requests, selected)
}

func assertRawOperationResults(t *testing.T, fake *fakePrivilegedHelper, data []byte) {
	t.Helper()
	if got := fake.writes.String(); got != "image" {
		t.Fatalf("helper write = %q, want %q", got, "image")
	}
	if got := string(data); got != "verified" {
		t.Fatalf("helper read = %q, want %q", got, "verified")
	}
	if got := len(fake.requests); got != 3 {
		t.Fatalf("helper request count = %d, want 3: %+v", got, fake.requests)
	}
}

func assertHelperRequests(t *testing.T, requests []privilegedRequest, selected device.Device) {
	t.Helper()
	for i, mode := range []operationMode{modeWrite, modeRead, modeFlush} {
		req := requests[i]
		identityMatches := req.Identity == selected.ID && req.Major == selected.Major && req.Minor == selected.Minor
		operationMatches := req.Capacity == selected.Size && req.Mode == mode
		if !identityMatches || !operationMatches {
			t.Fatalf("request %d = %+v", i, req)
		}
	}
}

func TestHelperFailureReplacementAndAuthorizationCancellation(t *testing.T) {
	t.Run("writer propagates helper failure", func(t *testing.T) {
		b, selected := helperFailureFixture(t, errors.New("helper failed"))
		if _, err := b.OpenWriter(context.Background(), selected); err == nil {
			t.Fatal("helper failure was ignored")
		}
	})
	t.Run("reader reports replacement detected by helper", func(t *testing.T) {
		b, selected := helperFailureFixture(t, ErrDeviceChanged)
		if _, err := b.OpenReader(context.Background(), selected); !errors.Is(err, ErrDeviceChanged) {
			t.Fatalf("replacement error = %v", err)
		}
	})
	t.Run("flush reports authorization cancellation", func(t *testing.T) {
		b, selected := helperFailureFixture(t, ErrAuthorizationCanceled)
		if err := b.Flush(context.Background(), selected); !errors.Is(err, ErrAuthorizationCanceled) {
			t.Fatalf("authorization error = %v", err)
		}
	})
	t.Run("replacement found before privilege escalation skips helper", func(t *testing.T) {
		b, selected := helperFailureFixture(t, nil)
		fake := b.helper.(*fakePrivilegedHelper)
		b.replaceIdentity("sdc", "NEWCARD", "ID_DRIVE_FLASH_SD")
		if _, err := b.OpenWriter(context.Background(), selected); !errors.Is(err, ErrDeviceChanged) {
			t.Fatalf("pre-helper replacement = %v", err)
		}
		if len(fake.requests) != 0 {
			t.Fatal("helper called after failed revalidation")
		}
	})
}

func helperFailureFixture(t *testing.T, helperErr error) (*backendFixture, device.Device) {
	t.Helper()
	b := newBackendFixture(t)
	selected, err := b.RefreshDevice(context.Background(), "CARD123")
	requireNoError(t, err)
	b.helper = &fakePrivilegedHelper{err: helperErr}
	return b, selected
}

func TestPrivilegedProtocolRejectsPathsAndUnknownModes(t *testing.T) {
	env := helperEnvironment{SysDevBlock: t.TempDir(), SysClassBlock: t.TempDir(), MountInfo: filepath.Join(t.TempDir(), "mountinfo"), Swaps: filepath.Join(t.TempDir(), "swaps"), DevRoot: t.TempDir()}
	for _, request := range []string{
		`{"identity":"CARD123","major":8,"minor":32,"capacity":33554432,"mode":"write","path":"/dev/sda"}`,
		`{"identity":"CARD123","major":8,"minor":32,"capacity":33554432,"mode":"erase"}`,
	} {
		if err := runPrivilegedHelper(strings.NewReader(request), io.Discard, io.Discard, env); err == nil {
			t.Fatalf("unsafe request accepted: %s", request)
		}
	}
}

func TestEmbeddedHelperInvocationRequiresExactPrivateArgument(t *testing.T) {
	if !IsEmbeddedHelperInvocation([]string{"usbwriter", embeddedHelperArgument}) {
		t.Fatal("exact embedded helper invocation rejected")
	}
	for _, args := range [][]string{{"usbwriter"}, {"usbwriter", embeddedHelperArgument, "extra"}, {"usbwriter", "--helper"}} {
		if IsEmbeddedHelperInvocation(args) {
			t.Fatalf("unexpected embedded helper invocation accepted: %#v", args)
		}
	}
}

func TestHelperCandidatesIncludeExecutableRelativeHelper(t *testing.T) {
	got := helperCandidates("/opt/goflasher/usr/bin/goflasher")
	want := []string{
		helperExecutable,
		"/opt/goflasher/usr/libexec/goflasher-helper",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("helper candidates = %#v, want %#v", got, want)
	}
}

func TestWriteProtocolPreservesBufferedBinaryPayloadAndSyncs(t *testing.T) {
	payload := append([]byte{0x00, 0xff, 0x7f, '\n'}, []byte("binary image payload")...)
	request := privilegedRequest{Identity: "SERIAL", Major: 8, Minor: 32, Capacity: uint64(len(payload)), Mode: modeWrite}
	var wire strings.Builder
	requestData, err := json.Marshal(request)
	requireNoError(t, err)
	_, err = wire.Write(requestData)
	requireNoError(t, err)
	_, err = wire.Write(payload)
	requireNoError(t, err)

	decoded, remaining, err := readPrivilegedRequest(strings.NewReader(wire.String()))
	requireNoError(t, err)
	if decoded != request {
		t.Fatalf("decoded request = %#v, want %#v", decoded, request)
	}
	target := &syncBuffer{}
	requireNoError(t, writeAndSync(target, remaining))
	if got := []byte(target.String()); !bytes.Equal(got, payload) {
		t.Fatalf("written payload = %x, want %x", got, payload)
	}
	if !target.synced {
		t.Fatal("write completed without syncing the target descriptor")
	}
}

func TestPrivilegedRequestFrameIsBounded(t *testing.T) {
	request := privilegedRequest{Identity: strings.Repeat("x", maxPrivilegedRequestBytes), Major: 8, Minor: 32, Capacity: 1, Mode: modeWrite}
	data, err := json.Marshal(request)
	requireNoError(t, err)
	if _, _, err := readPrivilegedRequest(bytes.NewReader(data)); err == nil {
		t.Fatal("oversized privileged request was accepted")
	}
}

func TestPrivilegedRequestNeedsNoPayloadDelimiter(t *testing.T) {
	request := privilegedRequest{Identity: "SERIAL", Major: 8, Minor: 32, Capacity: 1, Mode: modeWrite}
	data, err := json.Marshal(request)
	requireNoError(t, err)
	payload := []byte("payload-starts-immediately")
	decoded, remaining, err := readPrivilegedRequest(bytes.NewReader(append(data, payload...)))
	requireNoError(t, err)
	if decoded != request {
		t.Fatalf("decoded request = %#v, want %#v", decoded, request)
	}
	got, err := io.ReadAll(remaining)
	requireNoError(t, err)
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
}

func TestRequestRemainsCompatibleWithOlderHelperDecoder(t *testing.T) {
	request := privilegedRequest{Identity: "SERIAL", Major: 8, Minor: 32, Capacity: 1, Mode: modeWrite}
	var control bytes.Buffer
	requireNoError(t, sendRequest(nil, nopWriteCloser{&control}, request))
	if bytes.HasSuffix(control.Bytes(), []byte("\n")) {
		t.Fatal("request has a delimiter that an older helper would write to the device")
	}
	payload := []byte("image-payload")
	wire := bytes.NewReader(append(control.Bytes(), payload...))
	decoder := json.NewDecoder(wire)
	var decoded privilegedRequest
	requireNoError(t, decoder.Decode(&decoded))
	got, err := io.ReadAll(io.MultiReader(decoder.Buffered(), wire))
	requireNoError(t, err)
	if !bytes.Equal(got, payload) {
		t.Fatalf("older helper payload = %q, want %q", got, payload)
	}
}

func TestFlushSyncsBeforeInvalidatingBlockCache(t *testing.T) {
	target := &syncBuffer{}
	invalidated := false
	requireNoError(t, flushAndInvalidate(target, func() error {
		if !target.synced {
			t.Fatal("block cache invalidated before target sync")
		}
		invalidated = true
		return nil
	}))
	if !invalidated {
		t.Fatal("block cache was not invalidated")
	}
}

func TestFlushDoesNotInvalidateAfterSyncFailure(t *testing.T) {
	want := errors.New("sync failed")
	target := syncError{err: want}
	invalidated := false
	err := flushAndInvalidate(target, func() error {
		invalidated = true
		return nil
	})
	if !errors.Is(err, want) || invalidated {
		t.Fatalf("error = %v, invalidated = %v", err, invalidated)
	}
}

type syncError struct{ err error }

func (s syncError) Sync() error { return s.err }

func TestProgressParserHandlesFragmentedLines(t *testing.T) {
	var diagnostics strings.Builder
	updates := make(chan progress.Update, 1)
	parser := &progressParser{builder: &diagnostics, updates: updates}
	for _, chunk := range []string{"PROG", "RESS 25 100\ndiagnostic", " detail\nPROGRESS invalid\n"} {
		written, err := parser.Write([]byte(chunk))
		if err != nil {
			t.Fatalf("Write(%q) error = %v", chunk, err)
		}
		if written != len(chunk) {
			t.Fatalf("Write(%q) = %d bytes, want %d", chunk, written, len(chunk))
		}
	}
	update := <-updates
	assertFormattingProgress(t, update, 25, 100)
	if got, want := diagnostics.String(), "diagnostic detail\nPROGRESS invalid\n"; got != want {
		t.Fatalf("diagnostics = %q, want %q", got, want)
	}
}

func assertFormattingProgress(t *testing.T, update progress.Update, processed, total uint64) {
	t.Helper()
	if update.Stage != progress.StageFormatting {
		t.Fatalf("progress stage = %v, want %v", update.Stage, progress.StageFormatting)
	}
	if update.BytesProcessed != processed {
		t.Fatalf("processed bytes = %d, want %d", update.BytesProcessed, processed)
	}
	if update.TotalBytes != total {
		t.Fatalf("total bytes = %d, want %d", update.TotalBytes, total)
	}
}
