//go:build linux

package linux

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/goflasher/goflasher/internal/device"
	"golang.org/x/sys/unix"
)

const helperExecutable = "/usr/libexec/goflasher-helper"

var ErrAuthorizationCanceled = errors.New("authorization canceled")

type operationMode string

const (
	modeWrite operationMode = "write"
	modeRead  operationMode = "read-back"
	modeFlush operationMode = "flush"
)

// privilegedRequest deliberately has no device-path field. The privileged side
// derives both sysfs and /dev names from the expected kernel device number.
type privilegedRequest struct {
	Identity string        `json:"identity"`
	Serial   string        `json:"serial,omitempty"`
	WWN      string        `json:"wwn,omitempty"`
	Major    uint32        `json:"major"`
	Minor    uint32        `json:"minor"`
	Capacity uint64        `json:"capacity"`
	Mode     operationMode `json:"mode"`
}

func helperRequest(d device.Device, mode operationMode) privilegedRequest {
	return privilegedRequest{Identity: d.ID, Serial: d.Serial, WWN: d.WWN, Major: d.Major, Minor: d.Minor, Capacity: d.Size, Mode: mode}
}

type privilegedHelper interface {
	OpenWriter(context.Context, privilegedRequest) (io.WriteCloser, error)
	OpenReader(context.Context, privilegedRequest) (io.ReadCloser, error)
	Flush(context.Context, privilegedRequest) error
}

type commandHelper struct{ executable string }

func newCommandHelper() privilegedHelper { return &commandHelper{executable: helperExecutable} }

func (h *commandHelper) start(ctx context.Context, r privilegedRequest) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, "pkexec", h.executable)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return nil, nil, nil, authorizationError(err, stderr.String())
	}
	if err = json.NewEncoder(in).Encode(r); err != nil {
		_ = in.Close()
		_ = cmd.Wait()
		return nil, nil, nil, err
	}
	buffered := bufio.NewReader(out)
	line, err := buffered.ReadString('\n')
	if err != nil || line != "OK\n" {
		_ = in.Close()
		waitErr := cmd.Wait()
		if line != "" {
			stderr.WriteString(strings.TrimSpace(line))
		}
		if waitErr == nil {
			waitErr = err
		}
		return nil, nil, nil, authorizationError(waitErr, stderr.String())
	}
	return cmd, in, &bufferedReadCloser{Reader: buffered, closer: out}, nil
}

type bufferedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *bufferedReadCloser) Close() error { return r.closer.Close() }

func authorizationError(err error, detail string) error {
	if strings.Contains(strings.ToLower(detail), "cancel") || (err != nil && strings.Contains(err.Error(), "126")) {
		return fmt.Errorf("%w: %v", ErrAuthorizationCanceled, err)
	}
	if detail != "" {
		return fmt.Errorf("privileged helper: %s: %w", strings.TrimSpace(detail), err)
	}
	return err
}

type processWriter struct {
	io.WriteCloser
	cmd *exec.Cmd
}

func (w *processWriter) Close() error {
	a := w.WriteCloser.Close()
	b := w.cmd.Wait()
	if a != nil {
		return a
	}
	return b
}

type processReader struct {
	io.ReadCloser
	input io.WriteCloser
	cmd   *exec.Cmd
}

func (r *processReader) Close() error {
	a := r.ReadCloser.Close()
	_ = r.input.Close()
	// Verification normally reads only the image length, not unused space at the
	// end of the device. Terminate the one-shot helper if it is still streaming.
	_ = r.cmd.Process.Kill()
	_ = r.cmd.Wait()
	if a != nil {
		return a
	}
	return nil
}

func (h *commandHelper) OpenWriter(ctx context.Context, r privilegedRequest) (io.WriteCloser, error) {
	cmd, in, out, err := h.start(ctx, r)
	if out != nil {
		_ = out.Close()
	}
	if err != nil {
		return nil, err
	}
	return &processWriter{WriteCloser: in, cmd: cmd}, nil
}
func (h *commandHelper) OpenReader(ctx context.Context, r privilegedRequest) (io.ReadCloser, error) {
	cmd, in, out, err := h.start(ctx, r)
	if err != nil {
		return nil, err
	}
	_ = in.Close()
	return &processReader{ReadCloser: out, input: in, cmd: cmd}, nil
}
func (h *commandHelper) Flush(ctx context.Context, r privilegedRequest) error {
	cmd, in, out, err := h.start(ctx, r)
	if err != nil {
		return err
	}
	_ = in.Close()
	_ = out.Close()
	return cmd.Wait()
}

type helperEnvironment struct{ SysDevBlock, SysClassBlock, MountInfo, Swaps, DevRoot string }

func realHelperEnvironment() helperEnvironment {
	return helperEnvironment{"/sys/dev/block", "/sys/class/block", "/proc/self/mountinfo", "/proc/swaps", "/dev"}
}

// RunPrivilegedHelper serves exactly one authenticated operation over stdin/stdout.
func RunPrivilegedHelper(in io.Reader, out io.Writer, errOut io.Writer) error {
	return runPrivilegedHelper(in, out, errOut, realHelperEnvironment())
}

func runPrivilegedHelper(in io.Reader, out io.Writer, errOut io.Writer, env helperEnvironment) error {
	dec := json.NewDecoder(in)
	dec.DisallowUnknownFields()
	var req privilegedRequest
	if err := dec.Decode(&req); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	f, err := validateAndOpen(req, env)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = io.WriteString(out, "OK\n"); err != nil {
		return err
	}
	switch req.Mode {
	case modeWrite:
		_, err = io.Copy(f, dec.Buffered())
		if err == nil {
			_, err = io.Copy(f, in)
		}
	case modeRead:
		_, err = io.CopyN(out, f, int64(req.Capacity))
	case modeFlush:
		err = f.Sync()
	default:
		err = errors.New("unsupported operation mode")
	}
	return err
}

func validateAndOpen(req privilegedRequest, env helperEnvironment) (*os.File, error) {
	if req.Identity == "" || req.Capacity == 0 || (req.Mode != modeWrite && req.Mode != modeRead && req.Mode != modeFlush) {
		return nil, errors.New("incomplete request")
	}
	link := filepath.Join(env.SysDevBlock, fmt.Sprintf("%d:%d", req.Major, req.Minor))
	real, err := filepath.EvalSymlinks(link)
	if err != nil {
		return nil, ErrDeviceChanged
	}
	name := filepath.Base(real)
	class := filepath.Join(env.SysClassBlock, name)
	if exists(filepath.Join(class, "partition")) {
		return nil, ErrUnsupportedDevice
	}
	if readTrim(filepath.Join(class, "removable")) != "1" || readTrim(filepath.Join(class, "device/type")) != "0" || !strings.Contains(real, "/usb") {
		return nil, ErrUnsupportedDevice
	}
	maj, min, err := readDeviceNumber(filepath.Join(class, "dev"))
	if err != nil || maj != req.Major || min != req.Minor {
		return nil, ErrDeviceChanged
	}
	if readUint(filepath.Join(class, "size"))*512 != req.Capacity {
		return nil, ErrDeviceChanged
	}
	serial := readTrim(filepath.Join(class, "device/serial"))
	wwn := first(readTrim(filepath.Join(class, "wwid")), readTrim(filepath.Join(class, "device/wwid")))
	if req.Serial != "" && req.Serial != serial {
		return nil, ErrDeviceChanged
	}
	if req.WWN != "" && req.WWN != wwn {
		return nil, ErrDeviceChanged
	}
	derived := first(serial, wwn, fmt.Sprintf("%d:%d@%s", maj, min, real))
	if req.Identity != derived && req.Identity != req.Serial && req.Identity != req.WWN {
		return nil, ErrDeviceChanged
	}
	unsafe, err := mountedOrSystem(name, req.Major, req.Minor, env)
	if err != nil {
		return nil, err
	}
	if unsafe {
		return nil, ErrSystemDisk
	}
	node := filepath.Join(env.DevRoot, name)
	var st syscall.Stat_t
	if err := syscall.Stat(node, &st); err != nil {
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFBLK || unix.Major(uint64(st.Rdev)) != req.Major || unix.Minor(uint64(st.Rdev)) != req.Minor {
		return nil, ErrDeviceChanged
	}
	flags := os.O_RDONLY
	if req.Mode != modeRead {
		flags = os.O_WRONLY
	}
	return os.OpenFile(node, flags|syscall.O_CLOEXEC, 0)
}

func mountedOrSystem(name string, major, minor uint32, env helperEnvironment) (bool, error) {
	mounts, err := parseMountInfo(env.MountInfo)
	if err != nil {
		return false, err
	}
	critical := map[string]bool{"/": true, "/boot": true, "/boot/efi": true, "/home": true}
	for n, points := range mounts {
		parent := parentName(env.SysClassBlock, n)
		if (n.major == major && n.minor == minor) || parent == name {
			if len(points) != 0 {
				return true, nil
			}
			for _, p := range points {
				if critical[p] {
					return true, nil
				}
			}
		}
	}
	swaps, err := parseSwaps(env.Swaps)
	if err != nil {
		return false, err
	}
	for path := range swaps {
		if filepath.Base(path) == name || parentForPath(env.SysClassBlock, path) == name {
			return true, nil
		}
	}
	return false, nil
}
