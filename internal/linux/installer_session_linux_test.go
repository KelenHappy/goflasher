//go:build linux

package linux

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/goflasher/goflasher/internal/privilege"
	"golang.org/x/sys/unix"
)

func installerSessionFixture(t *testing.T) (*backendFixture, helperEnvironment, privilegedRequest, *os.File) {
	t.Helper()
	f := newBackendFixture(t)
	clearFixtureActivity(t, f.Backend)
	sysDev := filepath.Join(f.t.TempDir(), "sys/dev/block")
	requireNoError(t, os.MkdirAll(sysDev, 0755))
	real, err := filepath.EvalSymlinks(filepath.Join(f.SysClassBlock, "sdb"))
	requireNoError(t, err)
	requireNoError(t, os.Symlink(real, filepath.Join(sysDev, "8:16")))
	env := helperEnvironment{SysDevBlock: sysDev, SysClassBlock: f.SysClassBlock, MountInfo: f.MountInfo, Swaps: f.Swaps, DevRoot: f.DevRoot}
	env.fstat = func(_ int, st *syscall.Stat_t) error {
		st.Mode = syscall.S_IFBLK
		st.Rdev = unix.Mkdev(8, 16)
		return nil
	}
	// The fixture device is a regular file, which rejects BLKFLSBUF.
	env.invalidateCache = func(*os.File) error { return nil }
	target, err := os.OpenFile(filepath.Join(f.DevRoot, "sdb"), os.O_RDWR, 0)
	requireNoError(t, err)
	req := privilegedRequest{Version: privilege.ProtocolVersion, Identity: "FLASH123", Serial: "FLASH123", Major: 8, Minor: 16, Capacity: 65536 * 512, Mode: modeInstallerSession, LogicalSectorSize: 512}
	return f, env, req, target
}
func encodeSessionCommands(t *testing.T, commands ...any) *bytes.Reader {
	t.Helper()
	var b bytes.Buffer
	for _, value := range commands {
		switch v := value.(type) {
		case privilege.SessionCommand:
			data, err := json.Marshal(v)
			requireNoError(t, err)
			b.Write(data)
			b.WriteByte('\n')
		case []byte:
			b.Write(v)
		}
	}
	return bytes.NewReader(b.Bytes())
}

func TestInstallerSessionUsesBoundedRandomAccessAndStructuredReplies(t *testing.T) {
	_, env, req, target := installerSessionFixture(t)
	defer target.Close()
	payload := []byte("planned-block-data")
	input := encodeSessionCommands(t, privilege.SessionCommand{Version: privilege.ProtocolVersion, Kind: privilege.SessionWriteAt, Offset: 4096, Length: uint32(len(payload))}, payload, privilege.SessionCommand{Version: privilege.ProtocolVersion, Kind: privilege.SessionFlush}, privilege.SessionCommand{Version: privilege.ProtocolVersion, Kind: privilege.SessionClose})
	var output bytes.Buffer
	if err := runInstallerSession(installerSession{request: req, target: target, input: bufio.NewReader(input), output: &output, env: env}); err != nil {
		t.Fatal(err)
	}
	assertTargetBytes(t, target, 4096, payload)
	replies := decodeSessionReplies(t, &output)
	if len(replies) != 3 {
		t.Fatalf("replies=%d", len(replies))
	}
	for _, response := range replies {
		if !response.OK || response.Version != privilege.ProtocolVersion {
			t.Fatalf("response=%+v", response)
		}
	}
}

func TestInstallerSessionFlushInvalidatesBlockCache(t *testing.T) {
	_, env, req, target := installerSessionFixture(t)
	defer target.Close()
	var invalidated int
	env.invalidateCache = func(f *os.File) error {
		if f != target {
			t.Errorf("invalidated %v, want the session target", f.Name())
		}
		invalidated++
		return nil
	}
	input := encodeSessionCommands(t, privilege.SessionCommand{Version: privilege.ProtocolVersion, Kind: privilege.SessionFlush}, privilege.SessionCommand{Version: privilege.ProtocolVersion, Kind: privilege.SessionClose})
	var output bytes.Buffer
	if err := runInstallerSession(installerSession{request: req, target: target, input: bufio.NewReader(input), output: &output, env: env}); err != nil {
		t.Fatal(err)
	}
	if invalidated != 1 {
		t.Fatalf("cache invalidations=%d, want 1", invalidated)
	}
}

func TestInstallerSessionFlushReportsInvalidationFailure(t *testing.T) {
	_, env, req, target := installerSessionFixture(t)
	defer target.Close()
	env.invalidateCache = func(*os.File) error { return errors.New("BLKFLSBUF denied") }
	input := encodeSessionCommands(t, privilege.SessionCommand{Version: privilege.ProtocolVersion, Kind: privilege.SessionFlush})
	var output bytes.Buffer
	if err := runInstallerSession(installerSession{request: req, target: target, input: bufio.NewReader(input), output: &output, env: env}); err == nil {
		t.Fatal("flush succeeded without invalidating the cache")
	}
	replies := decodeSessionReplies(t, &output)
	if len(replies) != 1 || replies[0].OK || replies[0].Code != string(failOperationFailed) {
		t.Fatalf("replies=%+v", replies)
	}
}

func assertTargetBytes(t *testing.T, target *os.File, offset int64, want []byte) {
	t.Helper()
	got := make([]byte, len(want))
	if _, err := target.ReadAt(got, offset); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("payload=%q", got)
	}
}

// decodeSessionReplies parses the newline-delimited JSON replies of a session.
func decodeSessionReplies(t *testing.T, output io.Reader) []privilege.SessionResponse {
	t.Helper()
	var replies []privilege.SessionResponse
	scanner := bufio.NewScanner(output)
	for scanner.Scan() {
		var response privilege.SessionResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		replies = append(replies, response)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return replies
}

func TestInstallerSessionRejectsOutOfBoundsBeforeWrite(t *testing.T) {
	_, env, req, target := installerSessionFixture(t)
	defer target.Close()
	before, err := target.Stat()
	if err != nil {
		t.Fatal(err)
	}
	input := encodeSessionCommands(t, privilege.SessionCommand{Version: privilege.ProtocolVersion, Kind: privilege.SessionWriteAt, Offset: req.Capacity, Length: 1}, []byte{1})
	var output bytes.Buffer
	if err := runInstallerSession(installerSession{request: req, target: target, input: bufio.NewReader(input), output: &output, env: env}); err != nil {
		t.Fatal(err)
	}
	after, err := target.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatal("out-of-bounds command changed target")
	}
	var response privilege.SessionResponse
	if err = json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Code != "invalid-command" {
		t.Fatalf("response=%+v", response)
	}
}

func TestInstallerSessionRevalidatesIdentityOnFirstCommand(t *testing.T) {
	f, env, req, target := installerSessionFixture(t)
	defer target.Close()
	real, err := filepath.EvalSymlinks(filepath.Join(f.SysClassBlock, "sdb"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(real, "device/serial"), "REPLACED")
	input := encodeSessionCommands(t, privilege.SessionCommand{Version: privilege.ProtocolVersion, Kind: privilege.SessionFlush})
	var output bytes.Buffer
	if err := runInstallerSession(installerSession{request: req, target: target, input: bufio.NewReader(input), output: &output, env: env}); err == nil {
		t.Fatal("identity replacement accepted")
	}
	var response privilege.SessionResponse
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Code != "device-changed" {
		t.Fatalf("response=%+v", response)
	}
}

func TestInstallerSessionBoundsFullRevalidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		advance time.Duration
		length  uint64
	}{
		{name: "interval elapsed", advance: fullRevalidationInterval, length: 4096},
		{name: "byte bound reached", length: fullRevalidationBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, env, req, target := installerSessionFixture(t)
			defer target.Close()
			now := time.Unix(1_700_000_000, 0)
			s := installerSession{request: req, target: target, env: env, now: func() time.Time { return now }}
			requireNoError(t, s.revalidateTarget(4096))
			write(t, filepath.Join(f.SysClassBlock, "sdb", "device/serial"), "REPLACED")
			// Within both bounds only the descriptor is checked.
			requireNoError(t, s.revalidateTarget(4096))
			now = now.Add(tc.advance)
			if err := s.revalidateTarget(tc.length); !errors.Is(err, ErrDeviceChanged) {
				t.Fatalf("revalidateTarget() = %v, want %v", err, ErrDeviceChanged)
			}
		})
	}
}

func TestInstallerSessionChecksDescriptorBetweenFullRevalidations(t *testing.T) {
	_, env, req, target := installerSessionFixture(t)
	defer target.Close()
	calls := 0
	env.fstat = func(_ int, st *syscall.Stat_t) error {
		calls++
		st.Mode = syscall.S_IFBLK
		st.Rdev = unix.Mkdev(8, 16)
		if calls > 1 {
			st.Rdev = unix.Mkdev(8, 32)
		}
		return nil
	}
	s := installerSession{request: req, target: target, env: env}
	requireNoError(t, s.revalidateTarget(4096))
	if err := s.revalidateTarget(4096); !errors.Is(err, ErrDeviceChanged) {
		t.Fatalf("revalidateTarget() = %v, want %v", err, ErrDeviceChanged)
	}
}

func TestInstallerSessionSyncsAfterUnsyncedWriteLimit(t *testing.T) {
	_, env, req, target := installerSessionFixture(t)
	defer target.Close()
	s := installerSession{request: req, target: target, env: env, output: io.Discard}
	write := func(n uint64) {
		t.Helper()
		requireNoError(t, s.performWrite(privilege.SessionCommand{Offset: 0, Length: uint32(n)}, make([]byte, n)))
	}
	write(4096)
	if s.unsynced != 4096 {
		t.Fatalf("unsynced=%d after a small write", s.unsynced)
	}
	write(unsyncedWriteLimit - 4096)
	if s.unsynced != 0 {
		t.Fatalf("unsynced=%d after reaching the limit, want a sync", s.unsynced)
	}
}

func TestInstallerSessionClaimsDiskExclusively(t *testing.T) {
	for mode, exclusive := range map[operationMode]bool{
		modeInstallerSession: true, modeWrite: false, modeRead: false, modeFlush: false, modeFormatFAT32: false,
	} {
		if got := deviceOpenFlags(mode)&syscall.O_EXCL != 0; got != exclusive {
			t.Fatalf("%s opens with O_EXCL = %t, want %t", mode, got, exclusive)
		}
	}
}

// busyOpener fails with EBUSY the first busy times, then opens the fixture.
func busyOpener(f *backendFixture, busy int, attempts *int) func(string, int, os.FileMode) (*os.File, error) {
	return func(_ string, flag int, _ os.FileMode) (*os.File, error) {
		*attempts++
		if *attempts <= busy {
			return nil, &os.PathError{Op: "open", Path: "/dev/sdb", Err: syscall.EBUSY}
		}
		return os.OpenFile(filepath.Join(f.DevRoot, "sdb"), flag&^syscall.O_EXCL, 0)
	}
}

func TestInstallerSessionRetriesTransientExclusiveClaim(t *testing.T) {
	f, env, req, target := installerSessionFixture(t)
	_ = target.Close()
	var attempts, pauses int
	env.openFile = busyOpener(f, 3, &attempts)
	env.sleep = func(time.Duration) { pauses++ }
	opened, err := deviceGuard{req: req, env: env}.open(blockDevice{name: "sdb"})
	requireNoError(t, err)
	_ = opened.Close()
	if attempts != 4 || pauses != 3 {
		t.Fatalf("attempts = %d, pauses = %d, want 4 and 3", attempts, pauses)
	}
}

func TestInstallerSessionReportsPersistentClaimAsBusy(t *testing.T) {
	f, env, req, target := installerSessionFixture(t)
	_ = target.Close()
	var attempts int
	env.openFile = busyOpener(f, exclusiveOpenAttempts+10, &attempts)
	env.sleep = func(time.Duration) {}
	_, err := deviceGuard{req: req, env: env}.open(blockDevice{name: "sdb"})
	if !errors.Is(err, ErrDeviceBusy) || !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("open error = %v, want ErrDeviceBusy wrapping EBUSY", err)
	}
	if attempts != exclusiveOpenAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, exclusiveOpenAttempts)
	}
}

func TestNonExclusiveOpenDoesNotRetryBusy(t *testing.T) {
	f, env, req, target := installerSessionFixture(t)
	_ = target.Close()
	var attempts int
	env.openFile = busyOpener(f, 1, &attempts)
	env.sleep = func(time.Duration) { t.Fatal("non-exclusive open must not wait") }
	req.Mode = modeWrite
	if _, err := (deviceGuard{req: req, env: env}).open(blockDevice{name: "sdb"}); !errors.Is(err, ErrDeviceBusy) || attempts != 1 {
		t.Fatalf("open error = %v after %d attempts, want ErrDeviceBusy after 1", err, attempts)
	}
}
