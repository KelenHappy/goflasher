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
	in, out, err := commandPipes(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return nil, nil, nil, authorizationError(err, stderr.String())
	}
	if err = sendRequest(cmd, in, r); err != nil {
		return nil, nil, nil, err
	}
	buffered := bufio.NewReader(out)
	if err = awaitReady(cmd, in, buffered, &stderr); err != nil {
		return nil, nil, nil, err
	}
	return cmd, in, &bufferedReadCloser{Reader: buffered, closer: out}, nil
}

func commandPipes(cmd *exec.Cmd) (io.WriteCloser, io.ReadCloser, error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, nil, err
	}
	return in, out, nil
}

func sendRequest(cmd *exec.Cmd, in io.WriteCloser, request privilegedRequest) error {
	if err := json.NewEncoder(in).Encode(request); err != nil {
		_ = in.Close()
		_ = cmd.Wait()
		return err
	}
	return nil
}

func awaitReady(cmd *exec.Cmd, in io.WriteCloser, out *bufio.Reader, stderr *strings.Builder) error {
	line, readErr := out.ReadString('\n')
	if readErr == nil && line == "OK\n" {
		return nil
	}
	_ = in.Close()
	waitErr := cmd.Wait()
	if line != "" {
		stderr.WriteString(strings.TrimSpace(line))
	}
	if waitErr == nil {
		waitErr = readErr
	}
	return authorizationError(waitErr, stderr.String())
}

type bufferedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *bufferedReadCloser) Close() error { return r.closer.Close() }

func authorizationError(err error, detail string) error {
	if authorizationCanceled(err, detail) {
		return fmt.Errorf("%w: %v", ErrAuthorizationCanceled, err)
	}
	if detail != "" {
		return fmt.Errorf("privileged helper: %s: %w", strings.TrimSpace(detail), err)
	}
	return err
}

func authorizationCanceled(err error, detail string) bool {
	if strings.Contains(strings.ToLower(detail), "cancel") {
		return true
	}
	return err != nil && strings.Contains(err.Error(), "126")
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
	if !req.valid() {
		return nil, errors.New("incomplete request")
	}
	name, real, err := resolveDevice(req, env)
	if err != nil {
		return nil, err
	}
	class := filepath.Join(env.SysClassBlock, name)
	major, minor, err := validateDeviceMetadata(req, class, real)
	if err != nil {
		return nil, err
	}
	if err := validateDeviceSafety(env, name, major, minor); err != nil {
		return nil, err
	}
	return openDevice(req, env, name)
}

func (req privilegedRequest) valid() bool {
	if req.Identity == "" {
		return false
	}
	if req.Capacity == 0 {
		return false
	}
	switch req.Mode {
	case modeWrite, modeRead, modeFlush:
		return true
	default:
		return false
	}
}

func resolveDevice(req privilegedRequest, env helperEnvironment) (string, string, error) {
	link := filepath.Join(env.SysDevBlock, fmt.Sprintf("%d:%d", req.Major, req.Minor))
	real, err := filepath.EvalSymlinks(link)
	if err != nil {
		return "", "", ErrDeviceChanged
	}
	name := filepath.Base(real)
	class := filepath.Join(env.SysClassBlock, name)
	if exists(filepath.Join(class, "partition")) {
		return "", "", ErrUnsupportedDevice
	}
	if !supportedUSBDevice(class, real) {
		return "", "", ErrUnsupportedDevice
	}
	return name, real, nil
}

func supportedUSBDevice(class, real string) bool {
	if readTrim(filepath.Join(class, "removable")) != "1" {
		return false
	}
	if readTrim(filepath.Join(class, "device/type")) != "0" {
		return false
	}
	return strings.Contains(real, "/usb")
}

func validateDeviceMetadata(req privilegedRequest, class, real string) (uint32, uint32, error) {
	major, minor, err := readDeviceNumber(filepath.Join(class, "dev"))
	if err != nil {
		return 0, 0, ErrDeviceChanged
	}
	if major != req.Major || minor != req.Minor {
		return 0, 0, ErrDeviceChanged
	}
	if readUint(filepath.Join(class, "size"))*512 != req.Capacity {
		return 0, 0, ErrDeviceChanged
	}
	serial := readTrim(filepath.Join(class, "device/serial"))
	wwn := first(readTrim(filepath.Join(class, "wwid")), readTrim(filepath.Join(class, "device/wwid")))
	if req.Serial != "" && req.Serial != serial {
		return 0, 0, ErrDeviceChanged
	}
	if req.WWN != "" && req.WWN != wwn {
		return 0, 0, ErrDeviceChanged
	}
	derived := first(serial, wwn, fmt.Sprintf("%d:%d@%s", major, minor, real))
	if !identityMatches(req, derived) {
		return 0, 0, ErrDeviceChanged
	}
	return major, minor, nil
}

func identityMatches(req privilegedRequest, derived string) bool {
	if req.Identity == derived || req.Identity == req.Serial {
		return true
	}
	return req.Identity == req.WWN
}

func validateDeviceSafety(env helperEnvironment, name string, major, minor uint32) error {
	unsafe, err := mountedOrSystem(name, major, minor, env)
	if err != nil {
		return err
	}
	if unsafe {
		return ErrSystemDisk
	}
	return validateDeviceNode(env, name, major, minor)
}

func validateDeviceNode(env helperEnvironment, name string, major, minor uint32) error {
	node := filepath.Join(env.DevRoot, name)
	var st syscall.Stat_t
	if err := syscall.Stat(node, &st); err != nil {
		return err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFBLK {
		return ErrDeviceChanged
	}
	if unix.Major(uint64(st.Rdev)) != major || unix.Minor(uint64(st.Rdev)) != minor {
		return ErrDeviceChanged
	}
	return nil
}

func openDevice(req privilegedRequest, env helperEnvironment, name string) (*os.File, error) {
	flags := os.O_RDONLY
	if req.Mode != modeRead {
		flags = os.O_WRONLY
	}
	return os.OpenFile(filepath.Join(env.DevRoot, name), flags|syscall.O_CLOEXEC, 0)
}

func mountedOrSystem(name string, major, minor uint32, env helperEnvironment) (bool, error) {
	mounts, err := parseMountInfo(env.MountInfo)
	if err != nil {
		return false, err
	}
	if deviceMounted(name, major, minor, env.SysClassBlock, mounts) {
		return true, nil
	}
	swaps, err := parseSwaps(env.Swaps)
	if err != nil {
		return false, err
	}
	return deviceUsedForSwap(name, env.SysClassBlock, swaps), nil
}

func deviceMounted(name string, major, minor uint32, classRoot string, mounts map[devNumber][]string) bool {
	for number, points := range mounts {
		if len(points) == 0 {
			continue
		}
		if sameDevice(number, major, minor) || parentName(classRoot, number) == name {
			return true
		}
	}
	return false
}

func sameDevice(number devNumber, major, minor uint32) bool {
	return number.major == major && number.minor == minor
}

func deviceUsedForSwap(name, classRoot string, swaps map[string]bool) bool {
	for path := range swaps {
		if filepath.Base(path) == name || parentForPath(classRoot, path) == name {
			return true
		}
	}
	return false
}
