//go:build linux

package linux

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

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
	if err := runInstallerSession(req, target, input, &output, env); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := target.ReadAt(got, 4096); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload=%q", got)
	}
	scanner := bufio.NewScanner(&output)
	replies := 0
	for scanner.Scan() {
		var response privilege.SessionResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if !response.OK || response.Version != privilege.ProtocolVersion {
			t.Fatalf("response=%+v", response)
		}
		replies++
	}
	if replies != 3 {
		t.Fatalf("replies=%d", replies)
	}
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
	if err := runInstallerSession(req, target, input, &output, env); err != nil {
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

func TestInstallerSessionRevalidatesIdentityForEveryCommand(t *testing.T) {
	f, env, req, target := installerSessionFixture(t)
	defer target.Close()
	real, err := filepath.EvalSymlinks(filepath.Join(f.SysClassBlock, "sdb"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(real, "device/serial"), "REPLACED")
	input := encodeSessionCommands(t, privilege.SessionCommand{Version: privilege.ProtocolVersion, Kind: privilege.SessionFlush})
	var output bytes.Buffer
	if err := runInstallerSession(req, target, input, &output, env); err == nil {
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
