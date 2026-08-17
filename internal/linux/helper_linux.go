//go:build linux

package linux

import (
	"bufio"
	"bytes"
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
	"github.com/goflasher/goflasher/internal/fat32"
	"github.com/goflasher/goflasher/internal/progress"
	"golang.org/x/sys/unix"
)

const helperExecutable = "/usr/libexec/goflasher-helper"
const embeddedHelperArgument = "--goflasher-privileged-helper"

var ErrAuthorizationCanceled = errors.New("authorization canceled")

type operationMode string

const (
	modeWrite       operationMode = "write"
	modeRead        operationMode = "read-back"
	modeFlush       operationMode = "flush"
	modeFormatFAT32 operationMode = "format-fat32"
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
	Label    string        `json:"label,omitempty"`
}

func helperRequest(d device.Device, mode operationMode) privilegedRequest {
	return privilegedRequest{Identity: d.ID, Serial: d.Serial, WWN: d.WWN, Major: d.Major, Minor: d.Minor, Capacity: d.Size, Mode: mode}
}

type privilegedHelper interface {
	OpenWriter(context.Context, privilegedRequest) (io.WriteCloser, error)
	OpenReader(context.Context, privilegedRequest) (io.ReadCloser, error)
	Flush(context.Context, privilegedRequest) error
	FormatFAT32(context.Context, privilegedRequest, chan<- progress.Update) error
}

type commandHelper struct {
	executable string
	arguments  []string
}

func newCommandHelper() privilegedHelper {
	executable, err := os.Executable()
	if err != nil {
		executable = helperExecutable
	}
	for _, candidate := range helperCandidates(executable, os.Getenv("APPDIR")) {
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return &commandHelper{executable: candidate}
		}
	}
	return &commandHelper{executable: executable, arguments: []string{embeddedHelperArgument}}
}

func helperCandidates(executable, appDir string) []string {
	candidates := []string{helperExecutable}
	if appDir != "" {
		candidates = appendHelperCandidate(candidates, filepath.Join(appDir, "usr", "libexec", "goflasher-helper"))
	}
	if executable != "" {
		candidates = appendHelperCandidate(candidates, filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "libexec", "goflasher-helper")))
	}
	return candidates
}

func appendHelperCandidate(candidates []string, candidate string) []string {
	for _, existing := range candidates {
		if existing == candidate {
			return candidates
		}
	}
	return append(candidates, candidate)
}

// IsEmbeddedHelperInvocation reports whether this process was started by the
// GUI through pkexec to serve one privileged operation.
func IsEmbeddedHelperInvocation(args []string) bool {
	return len(args) == 2 && args[1] == embeddedHelperArgument
}

func (h *commandHelper) start(ctx context.Context, r privilegedRequest, updates chan<- progress.Update) (*exec.Cmd, io.WriteCloser, io.ReadCloser, *strings.Builder, error) {
	pkexecArgs := append([]string{h.executable}, h.arguments...)
	cmd := exec.CommandContext(ctx, "pkexec", pkexecArgs...)
	in, out, err := commandPipes(cmd)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stderr := &strings.Builder{}
	if updates != nil {
		cmd.Stderr = &progressParser{builder: stderr, updates: updates}
	} else {
		cmd.Stderr = stderr
	}
	if err = cmd.Start(); err != nil {
		return nil, nil, nil, stderr, authorizationError(err, stderr.String())
	}
	if err = sendRequest(cmd, in, r); err != nil {
		return nil, nil, nil, stderr, err
	}
	buffered := bufio.NewReader(out)
	if err = awaitReady(cmd, in, buffered, stderr); err != nil {
		return nil, nil, nil, stderr, err
	}
	return cmd, in, &bufferedReadCloser{Reader: buffered, closer: out}, stderr, nil
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
	data, err := json.Marshal(request)
	if err == nil {
		data = append(data, '\n')
		_, err = in.Write(data)
	}
	if err != nil {
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

type progressParser struct {
	builder *strings.Builder
	updates chan<- progress.Update
	buf     []byte
}

func (p *progressParser) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for line, ok := p.nextLine(); ok; line, ok = p.nextLine() {
		p.writeLine(line)
	}
	return len(b), nil
}

func (p *progressParser) nextLine() (string, bool) {
	idx := bytes.IndexByte(p.buf, '\n')
	if idx < 0 {
		return "", false
	}
	line := string(p.buf[:idx])
	p.buf = p.buf[idx+1:]
	return line, true
}

func (p *progressParser) writeLine(line string) {
	if update, ok := formattingProgress(line); ok {
		if p.updates != nil {
			p.updates <- update
		}
		return
	}
	p.builder.WriteString(line)
	p.builder.WriteByte('\n')
}

func formattingProgress(line string) (progress.Update, bool) {
	if !strings.HasPrefix(line, "PROGRESS ") {
		return progress.Update{}, false
	}
	var processed, total uint64
	if n, _ := fmt.Sscanf(line, "PROGRESS %d %d", &processed, &total); n != 2 {
		return progress.Update{}, false
	}
	return progress.Calculate(progress.StageFormatting, processed, total, 0), true
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
	cmd    *exec.Cmd
	stderr *strings.Builder
}

func (w *processWriter) Close() error {
	a := w.WriteCloser.Close()
	b := w.cmd.Wait()
	if a != nil {
		return a
	}
	if b == nil {
		return nil
	}
	return authorizationError(b, w.stderr.String())
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
	cmd, in, out, stderr, err := h.start(ctx, r, nil)
	if out != nil {
		_ = out.Close()
	}
	if err != nil {
		return nil, err
	}
	return &processWriter{WriteCloser: in, cmd: cmd, stderr: stderr}, nil
}
func (h *commandHelper) OpenReader(ctx context.Context, r privilegedRequest) (io.ReadCloser, error) {
	cmd, in, out, _, err := h.start(ctx, r, nil)
	if err != nil {
		return nil, err
	}
	_ = in.Close()
	return &processReader{ReadCloser: out, input: in, cmd: cmd}, nil
}
func (h *commandHelper) Flush(ctx context.Context, r privilegedRequest) error {
	cmd, in, out, _, err := h.start(ctx, r, nil)
	if err != nil {
		return err
	}
	_ = in.Close()
	_ = out.Close()
	return cmd.Wait()
}

func (h *commandHelper) FormatFAT32(ctx context.Context, r privilegedRequest, updates chan<- progress.Update) error {
	cmd, in, out, stderr, err := h.start(ctx, r, updates)
	if err != nil {
		return err
	}
	_ = in.Close()
	_ = out.Close()
	if err := cmd.Wait(); err != nil {
		return authorizationError(err, stderr.String())
	}
	return nil
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
	req, payload, err := readPrivilegedRequest(in)
	if err != nil {
		return err
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
		err = writeAndSync(f, payload)
	case modeRead:
		_, err = io.CopyN(out, f, int64(req.Capacity))
	case modeFlush:
		err = flushAndInvalidate(f, func() error { return invalidateBlockCache(f) })
	case modeFormatFAT32:
		err = makeFAT32(f, req.Capacity, req.Label, errOut)
	default:
		err = errors.New("unsupported operation mode")
	}
	return err
}

const maxPrivilegedRequestBytes = 4096

// readPrivilegedRequest keeps the JSON control plane bounded and separate from
// the untrusted binary image stream. This prevents a privileged helper from
// allocating an attacker-controlled amount of memory while decoding metadata.
func readPrivilegedRequest(in io.Reader) (privilegedRequest, io.Reader, error) {
	buffered := bufio.NewReaderSize(in, maxPrivilegedRequestBytes+1)
	line, err := buffered.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > maxPrivilegedRequestBytes {
		return privilegedRequest{}, nil, errors.New("privileged request is too large")
	}
	if err != nil {
		return privilegedRequest{}, nil, fmt.Errorf("invalid request frame: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var req privilegedRequest
	if err := decoder.Decode(&req); err != nil {
		return privilegedRequest{}, nil, fmt.Errorf("invalid request: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return privilegedRequest{}, nil, errors.New("invalid request: multiple JSON values")
	}
	return req, buffered, nil
}

func flushAndInvalidate(target interface{ Sync() error }, invalidate func() error) error {
	if err := target.Sync(); err != nil {
		return err
	}
	return invalidate()
}

func invalidateBlockCache(target *os.File) error {
	// Verification must read the USB media, not clean pages retained from the
	// preceding write. BLKFLSBUF invalidates this block device's buffer cache;
	// the helper runs with the privilege required by the ioctl.
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, target.Fd(), uintptr(unix.BLKFLSBUF), 0)
	if errno != 0 {
		return fmt.Errorf("invalidate block cache: %w", errno)
	}
	return nil
}

func writeAndSync(target interface {
	io.Writer
	Sync() error
}, payload io.Reader) error {
	if _, err := io.Copy(target, payload); err != nil {
		return err
	}
	return target.Sync()
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
	case modeFormatFAT32:
		return fat32.ValidLabel(req.Label)
	default:
		return false
	}
}

func makeFAT32(device *os.File, size uint64, label string, errOut io.Writer) error {
	return fat32.Format(context.Background(), device, size, label, func(percent uint64) { fmt.Fprintf(errOut, "PROGRESS %d 100\n", percent) })
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
	if major != req.Major {
		return 0, 0, ErrDeviceChanged
	}
	if minor != req.Minor {
		return 0, 0, ErrDeviceChanged
	}
	if readUint(filepath.Join(class, "size"))*512 != req.Capacity {
		return 0, 0, ErrDeviceChanged
	}
	if !readDeviceIdentities(class, real, major, minor).matches(req) {
		return 0, 0, ErrDeviceChanged
	}
	return major, minor, nil
}

type deviceIdentities struct {
	serials, wwns, all []string
}

func readDeviceIdentities(class, real string, major, minor uint32) deviceIdentities {
	serials := uniqueIdentityValues(readTrim(filepath.Join(class, "device/serial")), readUSBAncestorAttribute(real, "serial"))
	wwns := uniqueIdentityValues(readTrim(filepath.Join(class, "wwid")), readTrim(filepath.Join(class, "device/wwid")))
	all := append(append([]string{}, serials...), wwns...)
	all = append(all, fmt.Sprintf("%d:%d@%s", major, minor, real))
	return deviceIdentities{serials: serials, wwns: wwns, all: all}
}

func (ids deviceIdentities) matches(req privilegedRequest) bool {
	if req.Serial != "" && !containsIdentity(ids.serials, req.Serial) {
		return false
	}
	if req.WWN != "" && !containsIdentity(ids.wwns, req.WWN) {
		return false
	}
	return containsIdentity(ids.all, req.Identity)
}

func uniqueIdentityValues(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !containsIdentity(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func containsIdentity(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// Some USB flash drives expose their serial only on the USB device node, while
// udev propagates it to ID_SERIAL_SHORT for the block device. Walk to the first
// physical USB ancestor so privileged revalidation uses the same hardware
// identity without trusting data supplied by the GUI.
func readUSBAncestorAttribute(real, attribute string) string {
	for path := filepath.Clean(real); path != filepath.Dir(path); path = filepath.Dir(path) {
		if exists(filepath.Join(path, "idVendor")) && exists(filepath.Join(path, "idProduct")) {
			return readTrim(filepath.Join(path, attribute))
		}
	}
	return ""
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
	if req.Mode == modeFormatFAT32 {
		// The in-process formatter uses random-access writes and syncs the
		// completed filesystem, so its validated descriptor must be read/write.
		flags = os.O_RDWR
	} else if req.Mode == modeWrite {
		// Keep each large stream write tied to actual device progress instead of
		// letting Linux accept the entire image into the page cache at RAM speed.
		// Besides producing meaningful throughput/ETA figures, this bounds the
		// otherwise very long and apparently frozen Sync at the end of a write.
		flags = os.O_WRONLY | syscall.O_SYNC
	} else if req.Mode != modeRead {
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
